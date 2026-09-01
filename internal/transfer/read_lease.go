package transfer

import (
	"context"
	"sync"
)

type jobReadGate struct {
	accepting  bool
	generation uint64
	leases     map[*jobReadLease]struct{}
}

type jobReadLease struct {
	service    *Service
	jobID      string
	generation uint64
	done       chan struct{}
	finishOnce sync.Once
	mu         sync.Mutex
	handle     *ReadHandle
	canceled   bool
	stopCancel func() bool
}

func (s *Service) beginJobRead(ctx context.Context, jobID string) (*jobReadLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrServiceClosed
	}
	gate := s.jobReadGates[jobID]
	if gate == nil {
		gate = &jobReadGate{accepting: true, leases: make(map[*jobReadLease]struct{})}
		s.jobReadGates[jobID] = gate
	}
	if !gate.accepting {
		return nil, ErrNotFound
	}
	lease := &jobReadLease{service: s, jobID: jobID, generation: gate.generation, done: make(chan struct{})}
	lease.stopCancel = context.AfterFunc(ctx, lease.cancel)
	gate.leases[lease] = struct{}{}
	return lease, nil
}

func (lease *jobReadLease) attach(handle *ReadHandle) bool {
	lease.mu.Lock()
	if lease.canceled {
		lease.mu.Unlock()
		_ = handle.Close()
		lease.finish()
		return false
	}
	lease.handle = handle
	handle.onClose = lease.finish
	lease.mu.Unlock()
	return true
}

func (lease *jobReadLease) cancel() {
	lease.mu.Lock()
	lease.canceled = true
	handle := lease.handle
	lease.mu.Unlock()
	if handle != nil {
		_ = handle.Close()
	}
}

func (lease *jobReadLease) finish() {
	lease.finishOnce.Do(func() {
		if lease.stopCancel != nil {
			lease.stopCancel()
		}
		lease.service.mu.Lock()
		if gate := lease.service.jobReadGates[lease.jobID]; gate != nil {
			delete(gate.leases, lease)
			if gate.accepting && len(gate.leases) == 0 {
				delete(lease.service.jobReadGates, lease.jobID)
			}
		}
		lease.service.mu.Unlock()
		close(lease.done)
	})
}

func (s *Service) closeJobReads(ctx context.Context, jobID string) (uint64, error) {
	s.mu.Lock()
	gate := s.jobReadGates[jobID]
	if gate == nil {
		gate = &jobReadGate{leases: make(map[*jobReadLease]struct{})}
		s.jobReadGates[jobID] = gate
	}
	gate.accepting = false
	gate.generation++
	generation := gate.generation
	leases := make([]*jobReadLease, 0, len(gate.leases))
	for lease := range gate.leases {
		leases = append(leases, lease)
	}
	s.mu.Unlock()
	for _, lease := range leases {
		lease.cancel()
	}
	for _, lease := range leases {
		select {
		case <-lease.done:
		case <-ctx.Done():
			return generation, ctx.Err()
		}
	}
	return generation, nil
}

func (s *Service) reopenJobReads(jobID string, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate := s.jobReadGates[jobID]
	if gate != nil && gate.generation == generation && !s.closed {
		gate.accepting = true
		if len(gate.leases) == 0 {
			delete(s.jobReadGates, jobID)
		}
	}
}

func (s *Service) removeJobReadGate(jobID string) {
	s.mu.Lock()
	delete(s.jobReadGates, jobID)
	s.mu.Unlock()
}
