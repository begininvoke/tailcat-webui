package transfer

import (
	"context"
	"fmt"
	"sync"

	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
)

type ownerObjectKind uint8

const (
	ownerObjectShare ownerObjectKind = iota
	ownerObjectJob
)

func (s *Service) reserveOwnerObject(ctx context.Context, ownerID string, kind ownerObjectKind) (func(), error) {
	s.objectMu.Lock()
	defer s.objectMu.Unlock()
	var count, pending, limit int
	var err error
	switch kind {
	case ownerObjectShare:
		count, err = s.db.TransferShare.Query().Where(transfershare.UserIDEQ(ownerID)).Count(ctx)
		pending = s.pendingShares[ownerID]
		limit = s.limits.MaxSharesPerOwner
	case ownerObjectJob:
		count, err = s.db.TransferJob.Query().Where(transferjob.UserIDEQ(ownerID)).Count(ctx)
		pending = s.pendingJobs[ownerID]
		limit = s.limits.MaxRetainedJobsPerOwner
	default:
		return nil, fmt.Errorf("%w: unknown owner object kind", ErrInvalidState)
	}
	if err != nil {
		return nil, fmt.Errorf("count retained owner transfer objects: %w", err)
	}
	if count+pending >= limit {
		return nil, ErrOwnerCapacity
	}
	if kind == ownerObjectShare {
		s.pendingShares[ownerID]++
	} else {
		s.pendingJobs[ownerID]++
	}
	return sync.OnceFunc(func() {
		s.objectMu.Lock()
		defer s.objectMu.Unlock()
		pending := s.pendingJobs
		if kind == ownerObjectShare {
			pending = s.pendingShares
		}
		pending[ownerID]--
		if pending[ownerID] == 0 {
			delete(pending, ownerID)
		}
	}), nil
}
