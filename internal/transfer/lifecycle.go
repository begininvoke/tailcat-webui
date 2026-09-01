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

func (cause deletionCause) action() string {
	switch cause {
	case deletionExpired:
		return "transfer.expire"
	case deletionLimit:
		return "transfer.limit"
	default:
		return "transfer.delete"
	}
}

func (cause deletionCause) operation(kind string) string {
	switch cause {
	case deletionExpired:
		return kind + ".expire"
	case deletionLimit:
		return kind + ".limit"
	default:
		return kind + ".delete"
	}
}

func (s *Service) DeleteShare(ctx context.Context, ownerID, shareID string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	return s.deleteShare(ctx, ownerID, shareID, deletionRequested)
}

func (s *Service) deleteShare(ctx context.Context, ownerID, shareID string, cause deletionCause) (retErr error) {
	row, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load transfer share for deletion: %w", err)
	}
	unlock := s.lockShareOperation(shareID)
	defer unlock()
	row, err = s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("recheck transfer share for deletion: %w", err)
	}
	if row.Status == transfershare.StatusDeleting {
		switch row.ErrorCode {
		case transfershare.ErrorCodeTransferExpired:
			cause = deletionExpired
		case transfershare.ErrorCodeTransferLimitExceeded:
			cause = deletionLimit
		}
	}
	action := cause.action()
	generation, err := s.closeShareAdmission(ctx, shareID, ErrInvalidCapability)
	if err != nil {
		return err
	}
	if row.Status != transfershare.StatusDeleting {
		if !legalTransferTransition(string(row.Status), string(transfershare.StatusDeleting)) {
			s.reopenShareAdmissionIfLegal(ctx, ownerID, shareID, generation)
			return ErrInvalidState
		}
		update := row.Update().
			Where(transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(row.Status)).
			SetStatus(transfershare.StatusDeleting)
		switch cause {
		case deletionExpired:
			update.SetErrorCode(transfershare.ErrorCodeTransferExpired)
		case deletionLimit:
			update.SetErrorCode(transfershare.ErrorCodeTransferLimitExceeded)
		}
		row, err = update.Save(ctx)
		if ent.IsNotFound(err) {
			s.reopenShareAdmissionIfLegal(ctx, ownerID, shareID, generation)
			return ErrInvalidState
		}
		if err != nil {
			s.reopenShareAdmissionIfLegal(context.WithoutCancel(ctx), ownerID, shareID, generation)
			return fmt.Errorf("mark transfer share deleting: %w", err)
		}
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
		auditErr := s.recordLifecycle(ctx, ownerID, action+"_failed", "share", shareID, "failure")
		return errors.Join(fmt.Errorf("remove transfer share bytes: %w", err), auditErr)
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transfer share final deletion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, action, "share", shareID, "success"); err != nil {
		return err
	}
	deleted, err := tx.Client().TransferShare.Delete().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(transfershare.StatusDeleting)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete transfer share metadata: %w", err)
	}
	if deleted != 1 {
		return ErrInvalidState
	}
	operation := cause.operation("share")
	if err := s.commitLifecycle(tx, operation); err != nil {
		return fmt.Errorf("commit transfer share deletion: %w", err)
	}
	committed = true
	s.removeShareGate(shareID)
	s.publishTransfer(ownerID, shareID, events.RuntimePhaseStopped, TransferEventPayload{ShareID: shareID, Status: "deleted"})
	return nil
}

func (s *Service) DeleteJob(ctx context.Context, ownerID, jobID string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	return s.deleteJob(ctx, ownerID, jobID, deletionRequested)
}

func (s *Service) deleteJob(ctx context.Context, ownerID, jobID string, cause deletionCause) (retErr error) {
	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load transfer job for deletion: %w", err)
	}
	if row.Status == transferjob.StatusDeleting {
		switch row.ErrorCode {
		case transferjob.ErrorCodeTransferExpired:
			cause = deletionExpired
		case transferjob.ErrorCodeTransferLimitExceeded:
			cause = deletionLimit
		}
	}
	action := cause.action()
	if row.Status != transferjob.StatusDeleting {
		if !legalTransferTransition(string(row.Status), string(transferjob.StatusDeleting)) {
			return ErrInvalidState
		}
		update := row.Update().
			Where(transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(row.Status)).
			SetStatus(transferjob.StatusDeleting)
		switch cause {
		case deletionExpired:
			update.SetErrorCode(transferjob.ErrorCodeTransferExpired)
		case deletionLimit:
			update.SetErrorCode(transferjob.ErrorCodeTransferLimitExceeded)
		}
		row, err = update.Save(ctx)
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
		auditErr := s.recordLifecycle(ctx, ownerID, action+"_failed", "job", jobID, "failure")
		return errors.Join(fmt.Errorf("remove transfer job bytes: %w", err), auditErr)
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transfer job final deletion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, action, "job", jobID, "success"); err != nil {
		return err
	}
	deleted, err := tx.Client().TransferJob.Delete().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(transferjob.StatusDeleting)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete transfer job metadata: %w", err)
	}
	if deleted != 1 {
		return ErrInvalidState
	}
	operation := cause.operation("job")
	if err := s.commitLifecycle(tx, operation); err != nil {
		return fmt.Errorf("commit transfer job deletion: %w", err)
	}
	committed = true
	s.publishTransfer(ownerID, jobID, events.RuntimePhaseStopped, TransferEventPayload{JobID: jobID, Status: "deleted"})
	return nil
}

func (s *Service) interruptAbandoned(ctx context.Context) error {
	rows, err := s.db.TransferJob.Query().Where(transferjob.StatusEQ(transferjob.StatusRunning)).All(ctx)
	if err != nil {
		return fmt.Errorf("list abandoned transfer jobs: %w", err)
	}
	for _, row := range rows {
		updated, _, err := s.persistJobTerminalWithRetry(ctx, row.UserID, row.ID, transferjob.StatusInterrupted, transferitem.StatusInterrupted, transferjob.ErrorCodeTransferRemoteUnavailable)
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
			if err := s.deleteShare(ctx, share.UserID, share.ID, deletionRequested); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
			}
		} else if limitErr := s.validateShareLimits(ctx, share, now); limitErr != nil {
			cause, configured := configuredDeletionCause(limitErr)
			if !configured {
				errs = append(errs, limitErr)
				continue
			}
			if err := s.deleteShare(ctx, share.UserID, share.ID, cause); err != nil && !errors.Is(err, ErrNotFound) {
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
			if err := s.deleteJob(ctx, job.UserID, job.ID, deletionRequested); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		if limitErr := s.validateJobLimits(ctx, job, now); limitErr != nil {
			cause, configured := configuredDeletionCause(limitErr)
			if !configured {
				errs = append(errs, limitErr)
				continue
			}
			if err := s.deleteJob(ctx, job.UserID, job.ID, cause); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		if job.Status == transferjob.StatusInterrupted {
			if _, err := s.startJob(ctx, job.UserID, job.ID, true); err != nil {
				if errors.Is(err, ErrOwnerCapacity) || retryableResumeError(err) {
					s.enqueueResume(job.UserID, job.ID)
				}
				if !errors.Is(err, ErrOwnerCapacity) {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}
