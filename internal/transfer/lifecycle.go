package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
	"github.com/ca-x/tailcat-webui/ent/transferitem"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
	"github.com/ca-x/tailcat-webui/internal/events"
)

func legalTransferTransition(from, to string) bool {
	switch from {
	case "staging":
		return to == "ready" || to == "deleting" || to == "expired"
	case "ready":
		return to == "running" || to == "deleting" || to == "expired"
	case "running":
		return to == "completed" || to == "failed" || to == "canceled" || to == "interrupted" || to == "expired" || to == "deleting"
	case "failed", "canceled", "interrupted":
		return to == "running" || to == "deleting" || to == "expired"
	case "completed", "expired":
		return to == "deleting"
	case "deleting":
		return to == "deleting"
	default:
		return false
	}
}

func (s *Service) reconcileAudits(ctx context.Context) error {
	shares, err := s.db.TransferShare.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("list transfer-share audit reconciliation: %w", err)
	}
	for _, share := range shares {
		if err := s.recordLifecycle(ctx, share.UserID, "transfer.create", "share", share.ID, "success"); err != nil {
			return err
		}
	}
	jobs, err := s.db.TransferJob.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("list transfer-job audit reconciliation: %w", err)
	}
	for _, job := range jobs {
		if err := s.recordLifecycle(ctx, job.UserID, "transfer.create", "job", job.ID, "success"); err != nil {
			return err
		}
		if job.StartedAt != nil {
			if err := s.recordLifecycle(ctx, job.UserID, "transfer.start", "job", job.ID, "success"); err != nil {
				return err
			}
		}
		action, outcome := terminalAuditForJob(job.Status)
		if action != "" {
			if err := s.recordLifecycle(ctx, job.UserID, action, "job", job.ID, outcome); err != nil {
				return err
			}
		}
	}
	return nil
}

func terminalAuditForJob(status transferjob.Status) (string, string) {
	switch status {
	case transferjob.StatusCompleted:
		return "transfer.complete", "success"
	case transferjob.StatusCanceled:
		return "transfer.cancel", "success"
	case transferjob.StatusInterrupted:
		return "transfer.interrupt", "failure"
	case transferjob.StatusFailed:
		return "transfer.fail", "failure"
	case transferjob.StatusExpired:
		return "transfer.expire", "success"
	default:
		return "", ""
	}
}

func (s *Service) DeleteShare(ctx context.Context, ownerID, shareID string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	return s.deleteShare(ctx, ownerID, shareID, "transfer.delete")
}

func (s *Service) deleteShare(ctx context.Context, ownerID, shareID, action string) error {
	row, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load transfer share for deletion: %w", err)
	}
	if row.Status != transfershare.StatusDeleting {
		if !legalTransferTransition(string(row.Status), string(transfershare.StatusDeleting)) {
			return ErrInvalidState
		}
		row, err = row.Update().
			Where(transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(row.Status)).
			SetStatus(transfershare.StatusDeleting).
			Save(ctx)
		if ent.IsNotFound(err) {
			return ErrInvalidState
		}
		if err != nil {
			return fmt.Errorf("mark transfer share deleting: %w", err)
		}
	}
	if err := s.cancelShareStreamsAndWait(ctx, shareID, ErrInvalidCapability); err != nil {
		return err
	}
	files, err := s.db.ShareFile.Query().Where(sharefile.UserIDEQ(ownerID), sharefile.ShareIDEQ(shareID)).All(ctx)
	if err != nil {
		return fmt.Errorf("list transfer share files for deletion: %w", err)
	}
	var removeErrs []error
	for _, file := range files {
		if err := s.storage.Remove(ctx, ownerID, shareID, file.StorageName); err != nil {
			removeErrs = append(removeErrs, err)
		}
	}
	if err := errors.Join(removeErrs...); err != nil {
		_ = s.recordLifecycle(ctx, ownerID, action+"_failed", "share", shareID, "failure")
		return fmt.Errorf("remove transfer share bytes: %w", err)
	}
	if err := s.recordLifecycle(ctx, ownerID, action, "share", shareID, "success"); err != nil {
		return err
	}
	deleted, err := s.db.TransferShare.Delete().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(transfershare.StatusDeleting)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete transfer share metadata: %w", err)
	}
	if deleted != 1 {
		return ErrInvalidState
	}
	s.publishTransfer(ownerID, shareID, events.RuntimePhaseStopped, TransferEventPayload{ShareID: shareID, Status: "deleted"})
	return nil
}

func (s *Service) DeleteJob(ctx context.Context, ownerID, jobID string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	return s.deleteJob(ctx, ownerID, jobID, "transfer.delete")
}

func (s *Service) deleteJob(ctx context.Context, ownerID, jobID, action string) error {
	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load transfer job for deletion: %w", err)
	}
	if row.Status != transferjob.StatusDeleting {
		if !legalTransferTransition(string(row.Status), string(transferjob.StatusDeleting)) {
			return ErrInvalidState
		}
		row, err = row.Update().
			Where(transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(row.Status)).
			SetStatus(transferjob.StatusDeleting).
			Save(ctx)
		if ent.IsNotFound(err) {
			return ErrInvalidState
		}
		if err != nil {
			return fmt.Errorf("mark transfer job deleting: %w", err)
		}
	}
	s.mu.Lock()
	active := s.activeJobs[jobID]
	if active != nil && active.ownerID == ownerID {
		active.cancel(errCanceledByOwner)
	}
	s.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	items, err := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(ownerID), transferitem.JobIDEQ(jobID)).All(ctx)
	if err != nil {
		return fmt.Errorf("list transfer job items for deletion: %w", err)
	}
	var removeErrs []error
	for _, item := range items {
		if err := s.storage.Remove(ctx, ownerID, jobID, item.StorageName); err != nil {
			removeErrs = append(removeErrs, err)
		}
	}
	if err := errors.Join(removeErrs...); err != nil {
		_ = s.recordLifecycle(ctx, ownerID, action+"_failed", "job", jobID, "failure")
		return fmt.Errorf("remove transfer job bytes: %w", err)
	}
	if err := s.recordLifecycle(ctx, ownerID, action, "job", jobID, "success"); err != nil {
		return err
	}
	deleted, err := s.db.TransferJob.Delete().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(transferjob.StatusDeleting)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete transfer job metadata: %w", err)
	}
	if deleted != 1 {
		return ErrInvalidState
	}
	s.publishTransfer(ownerID, jobID, events.RuntimePhaseStopped, TransferEventPayload{JobID: jobID, Status: "deleted"})
	return nil
}

func (s *Service) cancelShareStreamsAndWait(ctx context.Context, shareID string, cause error) error {
	s.mu.Lock()
	streams := make([]*activeStream, 0, len(s.streams[shareID]))
	for stream := range s.streams[shareID] {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		stream.cancel(cause)
	}
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) interruptAbandoned(ctx context.Context) error {
	rows, err := s.db.TransferJob.Query().Where(transferjob.StatusEQ(transferjob.StatusRunning)).All(ctx)
	if err != nil {
		return fmt.Errorf("list abandoned transfer jobs: %w", err)
	}
	for _, row := range rows {
		if err := s.recordLifecycle(ctx, row.UserID, "transfer.interrupt", "job", row.ID, "failure"); err != nil {
			return err
		}
		updated, _, err := s.persistJobTerminal(ctx, row.UserID, row.ID, transferjob.StatusInterrupted, transferitem.StatusInterrupted, transferjob.ErrorCodeTransferRemoteUnavailable)
		if err != nil {
			return fmt.Errorf("interrupt abandoned transfer job: %w", err)
		}
		if updated {
			s.publishTransfer(row.UserID, row.ID, events.RuntimePhaseInterrupted, TransferEventPayload{JobID: row.ID, Status: string(transferjob.StatusInterrupted), ReceivedBytes: row.ReceivedBytes, TotalBytes: row.TotalBytes, ErrorCode: CodeRemoteUnavailable})
		}
	}
	return nil
}

// RecoverAfterRestore performs filesystem cleanup after Tailcat runtimes have
// been restored, then resumes unexpired interrupted incoming jobs.
func (s *Service) RecoverAfterRestore(ctx context.Context) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	now := time.Now().UTC()
	var errs []error
	shareFiles, err := s.db.ShareFile.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("list retained transfer share files: %w", err)
	}
	items, err := s.db.TransferItem.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("list retained transfer job items: %w", err)
	}
	retained := make([]StoredIdentity, 0, len(shareFiles)+len(items))
	for _, file := range shareFiles {
		retained = append(retained, StoredIdentity{OwnerID: file.UserID, ScopeID: file.ShareID, StorageName: file.StorageName})
	}
	for _, item := range items {
		retained = append(retained, StoredIdentity{OwnerID: item.UserID, ScopeID: item.JobID, StorageName: item.StorageName})
	}
	if _, err := s.storage.CleanupOrphans(ctx, retained); err != nil {
		errs = append(errs, fmt.Errorf("cleanup orphan transfer files: %w", err))
	}
	shares, err := s.db.TransferShare.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("list transfer shares for recovery: %w", err)
	}
	for _, share := range shares {
		if share.Status == transfershare.StatusDeleting {
			if err := s.deleteShare(ctx, share.UserID, share.ID, "transfer.delete"); err != nil {
				errs = append(errs, err)
			}
		} else if !share.ExpiresAt.After(now) {
			if err := s.deleteShare(ctx, share.UserID, share.ID, "transfer.expire"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	jobs, err := s.db.TransferJob.Query().All(ctx)
	if err != nil {
		return errors.Join(errors.Join(errs...), fmt.Errorf("list transfer jobs for recovery: %w", err))
	}
	for _, job := range jobs {
		if job.Status == transferjob.StatusDeleting {
			if err := s.deleteJob(ctx, job.UserID, job.ID, "transfer.delete"); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !job.ExpiresAt.After(now) {
			if err := s.deleteJob(ctx, job.UserID, job.ID, "transfer.expire"); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if job.Status == transferjob.StatusInterrupted {
			if _, err := s.ResumeJob(ctx, job.UserID, job.ID); err != nil {
				if errors.Is(err, ErrOwnerCapacity) {
					s.enqueueResume(job.UserID, job.ID)
				} else {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}
