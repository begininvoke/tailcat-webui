package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
)

const (
	expiryCleanupBatch = 32
	expiryRetryDelay   = time.Second
)

func (s *Service) wakeExpiryScheduler() {
	select {
	case s.expiryWake <- struct{}{}:
	default:
	}
}

func (s *Service) runExpiryScheduler() {
	for {
		deadline, ok, err := s.nextExpiryDeadline(s.expiryCtx)
		if err != nil {
			s.logger.ErrorContext(s.expiryCtx, "Load next transfer expiry failed", "error", err)
			if !s.waitForExpiryWake(expiryRetryDelay) {
				return
			}
			continue
		}
		if ok && !deadline.After(time.Now().UTC()) {
			removed, cleanupErr := s.expireDueResources(s.expiryCtx, time.Now().UTC())
			if cleanupErr != nil {
				s.logger.ErrorContext(s.expiryCtx, "Expire transfer resources failed", "error", cleanupErr)
				if !s.waitForExpiryWake(expiryRetryDelay) {
					return
				}
				continue
			}
			if removed > 0 {
				continue
			}
		}
		wait := time.Hour
		if ok {
			wait = max(time.Millisecond, time.Until(deadline))
		}
		if !s.waitForExpiryWake(wait) {
			return
		}
	}
}

func (s *Service) waitForExpiryWake(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.expiryWake:
		return true
	case <-s.expiryCtx.Done():
		return false
	}
}

func (s *Service) nextExpiryDeadline(ctx context.Context) (time.Time, bool, error) {
	var deadline time.Time
	consider := func(candidate time.Time) {
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline = candidate
		}
	}
	share, err := s.db.TransferShare.Query().Order(ent.Asc(transfershare.FieldExpiresAt)).First(ctx)
	if err == nil {
		consider(s.effectiveExpiry(share.CreatedAt, share.ExpiresAt))
	} else if !ent.IsNotFound(err) {
		return time.Time{}, false, fmt.Errorf("load next transfer share expiry: %w", err)
	}
	share, err = s.db.TransferShare.Query().Order(ent.Asc(transfershare.FieldCreatedAt)).First(ctx)
	if err == nil {
		consider(s.effectiveExpiry(share.CreatedAt, share.ExpiresAt))
	} else if !ent.IsNotFound(err) {
		return time.Time{}, false, fmt.Errorf("load next configured transfer share expiry: %w", err)
	}
	job, err := s.db.TransferJob.Query().Order(ent.Asc(transferjob.FieldExpiresAt)).First(ctx)
	if err == nil {
		consider(s.effectiveExpiry(job.CreatedAt, job.ExpiresAt))
	} else if !ent.IsNotFound(err) {
		return time.Time{}, false, fmt.Errorf("load next transfer job expiry: %w", err)
	}
	job, err = s.db.TransferJob.Query().Order(ent.Asc(transferjob.FieldCreatedAt)).First(ctx)
	if err == nil {
		consider(s.effectiveExpiry(job.CreatedAt, job.ExpiresAt))
	} else if !ent.IsNotFound(err) {
		return time.Time{}, false, fmt.Errorf("load next configured transfer job expiry: %w", err)
	}
	return deadline, !deadline.IsZero(), nil
}

func (s *Service) expireDueResources(ctx context.Context, now time.Time) (int, error) {
	configuredCutoff := now.Add(-s.limits.Expiry).In(time.Local)
	shares, err := s.db.TransferShare.Query().Where(transfershare.Or(
		transfershare.ExpiresAtLTE(now),
		transfershare.CreatedAtLTE(configuredCutoff),
	)).Order(ent.Asc(transfershare.FieldExpiresAt)).Limit(expiryCleanupBatch).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("list due transfer shares: %w", err)
	}
	jobs, err := s.db.TransferJob.Query().Where(transferjob.Or(
		transferjob.ExpiresAtLTE(now),
		transferjob.CreatedAtLTE(configuredCutoff),
	)).Order(ent.Asc(transferjob.FieldExpiresAt)).Limit(expiryCleanupBatch).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("list due transfer jobs: %w", err)
	}
	removed := 0
	var cleanupErrs []error
	for _, share := range shares {
		if err := s.deleteShare(ctx, share.UserID, share.ID, deletionExpired); err != nil && !errors.Is(err, ErrNotFound) {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		removed++
	}
	for _, job := range jobs {
		if err := s.deleteJob(ctx, job.UserID, job.ID, deletionExpired); err != nil && !errors.Is(err, ErrNotFound) {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(cleanupErrs...)
}
