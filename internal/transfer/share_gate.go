package transfer

import (
	"context"
	"errors"
	"sync"
	"time"
)

type shareOperationLock struct {
	mu   sync.Mutex
	refs int
}

var errShareExpired = errors.New("transfer share expired")

func (s *Service) lockShareOperation(shareID string) func() {
	s.mu.Lock()
	operation := s.shareOps[shareID]
	if operation == nil {
		operation = new(shareOperationLock)
		s.shareOps[shareID] = operation
	}
	operation.refs++
	s.mu.Unlock()
	operation.mu.Lock()
	return sync.OnceFunc(func() {
		operation.mu.Unlock()
		s.mu.Lock()
		operation.refs--
		if operation.refs == 0 {
			delete(s.shareOps, shareID)
		}
		s.mu.Unlock()
	})
}

func (s *Service) beginShareAdmission(parent context.Context, shareID string) (*activeStream, error) {
	ctx, cancel := context.WithCancelCause(parent)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		cancel(errServiceClosed)
		return nil, protocolError(CodeRemoteUnavailable, ErrServiceClosed)
	}
	gate := s.shareGates[shareID]
	if gate == nil {
		gate = &shareGate{
			accepting:   true,
			provisional: make(map[*activeStream]struct{}),
			active:      make(map[*activeStream]struct{}),
		}
		s.shareGates[shareID] = gate
	}
	if !gate.accepting {
		cancel(ErrInvalidCapability)
		return nil, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	stream := &activeStream{
		shareID: shareID, generation: gate.generation, ctx: ctx, cancel: cancel,
		done: make(chan struct{}),
	}
	gate.provisional[stream] = struct{}{}
	return stream, nil
}

func (s *Service) armShareExpiry(stream *activeStream, expiresAt time.Time) error {
	if !expiresAt.After(time.Now()) {
		return protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	s.mu.Lock()
	gate := s.shareGates[stream.shareID]
	if s.closed || gate == nil || !gate.accepting || gate.generation != stream.generation {
		s.mu.Unlock()
		return protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	if gate.expiry != nil {
		s.mu.Unlock()
		return nil
	}
	expiryCtx, cancel := context.WithDeadlineCause(context.Background(), expiresAt, errShareExpired)
	task := &shareExpiryTask{expiresAt: expiresAt, cancel: cancel, done: make(chan struct{})}
	gate.expiry = task
	hook := s.handlerHooks.afterExpiryArmed
	s.mu.Unlock()
	go func() {
		defer close(task.done)
		<-expiryCtx.Done()
		if errors.Is(context.Cause(expiryCtx), errShareExpired) {
			s.expireShareAdmission(stream.shareID, gate)
		}
	}()
	if hook != nil {
		hook(stream.shareID, func() { s.expireShareAdmission(stream.shareID, gate) })
	}
	return nil
}

func (s *Service) expireShareAdmission(shareID string, gate *shareGate) {
	_, _ = s.closeInstalledShareAdmission(context.Background(), shareID, gate, protocolError(CodeExpired, errShareExpired), true)
}

func (s *Service) commitShareAdmission(stream *activeStream, expiresAt time.Time) (context.Context, error) {
	if !expiresAt.After(time.Now()) {
		return nil, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	serveCtx, cancelServe := context.WithDeadlineCause(stream.ctx, expiresAt, protocolError(CodeExpired, errShareExpired))
	s.mu.Lock()
	gate := s.shareGates[stream.shareID]
	if s.closed || gate == nil || !gate.accepting || gate.generation != stream.generation {
		s.mu.Unlock()
		cancelServe()
		return nil, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	if _, exists := gate.provisional[stream]; !exists {
		s.mu.Unlock()
		cancelServe()
		return nil, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	delete(gate.provisional, stream)
	gate.active[stream] = struct{}{}
	stream.serveCtx = serveCtx
	stream.cancelServe = cancelServe
	s.mu.Unlock()
	return serveCtx, nil
}

func (s *Service) finishShareAdmission(stream *activeStream) {
	if stream == nil {
		return
	}
	stream.finishOnce.Do(func() {
		stream.cancel(nil)
		if stream.cancelServe != nil {
			stream.cancelServe()
		}
		s.mu.Lock()
		if gate := s.shareGates[stream.shareID]; gate != nil {
			delete(gate.provisional, stream)
			delete(gate.active, stream)
			if gate.accepting && gate.expiry == nil && len(gate.provisional) == 0 && len(gate.active) == 0 {
				delete(s.shareGates, stream.shareID)
			}
		}
		s.mu.Unlock()
		close(stream.done)
	})
}

func (s *Service) closeShareAdmission(ctx context.Context, shareID string, cause error) (uint64, error) {
	return s.closeInstalledShareAdmission(ctx, shareID, nil, cause, false)
}

func (s *Service) closeInstalledShareAdmission(ctx context.Context, shareID string, expected *shareGate, cause error, onlyIfAccepting bool) (uint64, error) {
	s.mu.Lock()
	gate := s.shareGates[shareID]
	if expected != nil && gate != expected {
		s.mu.Unlock()
		return 0, nil
	}
	if gate == nil {
		if expected != nil {
			s.mu.Unlock()
			return 0, nil
		}
		gate = &shareGate{
			accepting:   false,
			provisional: make(map[*activeStream]struct{}),
			active:      make(map[*activeStream]struct{}),
		}
		s.shareGates[shareID] = gate
	} else {
		if onlyIfAccepting && !gate.accepting {
			generation := gate.generation
			s.mu.Unlock()
			return generation, nil
		}
		gate.accepting = false
	}
	gate.generation++
	generation := gate.generation
	streams := make([]*activeStream, 0, len(gate.provisional)+len(gate.active))
	for stream := range gate.provisional {
		streams = append(streams, stream)
	}
	for stream := range gate.active {
		streams = append(streams, stream)
	}
	hook := s.handlerHooks.afterRevocationClosed
	s.mu.Unlock()
	if hook != nil {
		hook(shareID)
	}
	for _, stream := range streams {
		stream.cancel(cause)
	}
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-ctx.Done():
			return generation, ctx.Err()
		}
	}
	return generation, nil
}

func (s *Service) reopenShareAdmission(shareID string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate := s.shareGates[shareID]
	if gate == nil || s.closed || gate.generation != generation {
		return false
	}
	gate.accepting = true
	return true
}

func (s *Service) removeShareGate(shareID string) {
	s.mu.Lock()
	gate := s.shareGates[shareID]
	if gate == nil {
		s.mu.Unlock()
		return
	}
	gate.accepting = false
	generation := gate.generation
	expiry := gate.expiry
	s.mu.Unlock()
	if expiry != nil {
		if s.handlerHooks.beforeGateExpiryCancel != nil {
			s.handlerHooks.beforeGateExpiryCancel(shareID)
		}
		expiry.cancel()
		<-expiry.done
	}
	s.mu.Lock()
	if current := s.shareGates[shareID]; current == gate && current.generation == generation && len(current.provisional) == 0 && len(current.active) == 0 {
		delete(s.shareGates, shareID)
	}
	s.mu.Unlock()
}

func (s *Service) activeShareStreamsLocked(shareID string) int {
	gate := s.shareGates[shareID]
	if gate == nil {
		return 0
	}
	return len(gate.active)
}
