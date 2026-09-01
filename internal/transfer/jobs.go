package transfer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/tailclient"
	"github.com/ca-x/tailcat-webui/ent/transferitem"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/internal/events"

	"github.com/zeebo/blake3"
)

type CreateIncomingJobInput struct {
	ClientID   string
	Capability string
	ExpiresAt  time.Time
}

type JobView struct {
	ID            string    `json:"id"`
	ClientID      string    `json:"client_id"`
	RemoteShareID string    `json:"remote_share_id"`
	Status        string    `json:"status"`
	TotalBytes    int64     `json:"total_bytes"`
	ReceivedBytes int64     `json:"received_bytes"`
	ExpiresAt     time.Time `json:"expires_at"`
	ErrorCode     ErrorCode `json:"error_code,omitempty"`
}

type ItemView struct {
	ID              string    `json:"id"`
	JobID           string    `json:"job_id"`
	VirtualPath     string    `json:"virtual_path"`
	Size            int64     `json:"size"`
	Status          string    `json:"status"`
	ReceivedBytes   int64     `json:"received_bytes"`
	CompletedBlocks int       `json:"completed_blocks"`
	MTime           time.Time `json:"mtime"`
}

type preparedIncomingItem struct {
	id          string
	storageName string
	manifest    FileManifest
}

func (s *Service) CreateIncomingJob(ctx context.Context, ownerID string, input CreateIncomingJobInput) (_ JobView, retErr error) {
	if err := s.ensureOpen(); err != nil {
		return JobView{}, err
	}
	if validateEntityID(ownerID) != nil || validateEntityID(input.ClientID) != nil {
		return JobView{}, ErrNotFound
	}
	if _, err := s.db.TailClient.Query().Where(tailclient.IDEQ(input.ClientID), tailclient.UserIDEQ(ownerID)).Only(ctx); ent.IsNotFound(err) {
		return JobView{}, ErrNotFound
	} else if err != nil {
		return JobView{}, fmt.Errorf("validate transfer client ownership: %w", err)
	}
	capability := []byte(input.Capability)
	s.captureSecret("incoming.capability", capability)
	defer clearSecret(capability)
	parsed, err := parseCapabilityBytes(capability)
	if err != nil {
		return JobView{}, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	s.captureSecret("incoming.secret", parsed.secret[:])
	defer parsed.clear()
	jobID := newEntityID()
	ciphertext, err := s.box.Seal(capability, jobCapabilityAAD(ownerID, jobID))
	if err != nil {
		return JobView{}, fmt.Errorf("encrypt incoming transfer capability: %w", err)
	}
	manifest, err := fetchManifestSecret(ctx, func(dialCtx context.Context) (net.Conn, error) {
		return s.dialer.DialPort(dialCtx, ownerID, input.ClientID, ReservedPort)
	}, parsed.shareID, capability)
	if err != nil {
		return JobView{}, err
	}
	files := manifest.Files()
	var totalBytes int64
	for _, file := range files {
		if totalBytes > MaxShareBytes-file.Size() {
			return JobView{}, protocolError(CodeLimitExceeded, errors.New("incoming manifest exceeds job limit"))
		}
		totalBytes += file.Size()
	}
	now := time.Now().UTC()
	expiresAt := input.ExpiresAt.UTC()
	if input.ExpiresAt.IsZero() {
		expiresAt = now.Add(defaultTransferExpiry)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(defaultTransferExpiry)) {
		return JobView{}, ErrInvalidState
	}

	prepared := make([]preparedIncomingItem, 0, len(files))
	cleanup := true
	defer func() {
		if cleanup {
			retErr = errors.Join(retErr, s.cleanupPreparedPartials(ownerID, jobID, prepared))
		}
	}()
	for _, file := range files {
		partial, err := s.storage.CreatePartial(ctx, ownerID, jobID, file.Size())
		if err != nil {
			if partial != nil {
				prepared = append(prepared, preparedIncomingItem{id: newEntityID(), storageName: partial.StorageName(), manifest: file})
				_ = partial.Close()
			}
			return JobView{}, fmt.Errorf("create incoming partial: %w", err)
		}
		item := preparedIncomingItem{id: newEntityID(), storageName: partial.StorageName(), manifest: file}
		prepared = append(prepared, item)
		if err := partial.Close(); err != nil {
			return JobView{}, fmt.Errorf("close incoming partial: %w", err)
		}
	}

	s.metadataMu.Lock()
	metadataLocked := true
	defer func() {
		if metadataLocked {
			s.metadataMu.Unlock()
		}
	}()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return JobView{}, fmt.Errorf("begin incoming job transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	row, err := tx.TransferJob.Create().
		SetID(jobID).
		SetUserID(ownerID).
		SetClientID(input.ClientID).
		SetRemoteShareID(parsed.shareID).
		SetRemoteCapabilityCipher(ciphertext).
		SetStatus(transferjob.StatusReady).
		SetTotalBytes(totalBytes).
		SetReceivedBytes(0).
		SetExpiresAt(expiresAt).
		Save(ctx)
	if err != nil {
		return JobView{}, fmt.Errorf("create incoming transfer job: %w", err)
	}
	for _, item := range prepared {
		file := item.manifest
		if _, err := tx.TransferItem.Create().
			SetID(item.id).
			SetUserID(ownerID).
			SetJobID(jobID).
			SetRemoteFileID(file.FileID()).
			SetStorageName(item.storageName).
			SetVirtualPath(file.VirtualPath()).
			SetSizeBytes(file.Size()).
			SetMtime(file.MTime().UTC()).
			SetBlake3(file.BLAKE3()).
			SetBlockSize(BlockSize).
			SetBlockHashes(file.BlockHashes()).
			SetCompletedBlocks([]int{}).
			SetReceivedBytes(0).
			SetStatus(transferitem.StatusReady).
			Save(ctx); err != nil {
			return JobView{}, fmt.Errorf("create incoming transfer item: %w", err)
		}
	}
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, "transfer.create", "job", jobID, "success"); err != nil {
		return JobView{}, err
	}
	if err := s.commitLifecycle(tx, "job.create"); err != nil {
		return JobView{}, fmt.Errorf("commit incoming transfer job: %w", err)
	}
	committed = true
	metadataLocked = false
	s.metadataMu.Unlock()
	cleanup = false
	s.publishTransfer(ownerID, jobID, events.RuntimePhaseReady, TransferEventPayload{JobID: jobID, Status: string(row.Status), TotalBytes: row.TotalBytes, TotalFiles: len(prepared)})
	return jobView(row), nil
}

func (s *Service) cleanupPreparedPartials(ownerID, jobID string, prepared []preparedIncomingItem) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var errs []error
	for _, item := range prepared {
		if item.storageName == "" {
			continue
		}
		if err := s.storage.Remove(cleanupCtx, ownerID, jobID, item.storageName); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) Job(ctx context.Context, ownerID, jobID string) (JobView, error) {
	if err := s.ensureOpen(); err != nil {
		return JobView{}, err
	}
	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return JobView{}, ErrNotFound
	}
	if err != nil {
		return JobView{}, fmt.Errorf("load transfer job: %w", err)
	}
	return jobView(row), nil
}

func (s *Service) ListJobs(ctx context.Context, ownerID string) ([]JobView, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := s.db.TransferJob.Query().Where(transferjob.UserIDEQ(ownerID)).Order(ent.Desc(transferjob.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transfer jobs: %w", err)
	}
	views := make([]JobView, len(rows))
	for index, row := range rows {
		views[index] = jobView(row)
	}
	return views, nil
}

func (s *Service) ListJobItems(ctx context.Context, ownerID, jobID string) ([]ItemView, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if exists, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Exist(ctx); err != nil {
		return nil, err
	} else if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(ownerID), transferitem.JobIDEQ(jobID)).Order(ent.Asc(transferitem.FieldCreatedAt), ent.Asc(transferitem.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transfer job items: %w", err)
	}
	views := make([]ItemView, len(rows))
	for index, row := range rows {
		views[index] = ItemView{
			ID: row.ID, JobID: row.JobID, VirtualPath: row.VirtualPath, Size: row.SizeBytes,
			Status: string(row.Status), ReceivedBytes: row.ReceivedBytes, CompletedBlocks: len(row.CompletedBlocks), MTime: row.Mtime.UTC(),
		}
	}
	return views, nil
}

func (s *Service) StartJob(ctx context.Context, ownerID, jobID string) (JobView, error) {
	return s.startJob(ctx, ownerID, jobID, false)
}

func (s *Service) startJob(ctx context.Context, ownerID, jobID string, resumeManaged bool) (_ JobView, retErr error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return JobView{}, ErrServiceClosed
	}
	s.pending++
	s.mu.Unlock()
	pending := true
	defer func() {
		if pending {
			s.leavePending()
		}
	}()

	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return JobView{}, ErrNotFound
	}
	if err != nil {
		return JobView{}, fmt.Errorf("load startable transfer job: %w", err)
	}
	if !slices.Contains([]transferjob.Status{transferjob.StatusReady, transferjob.StatusInterrupted, transferjob.StatusFailed, transferjob.StatusCanceled}, row.Status) {
		return JobView{}, ErrInvalidState
	}
	if !row.ExpiresAt.After(time.Now().UTC()) {
		return JobView{}, ErrInvalidState
	}
	expiryCtx, stopExpiry := context.WithDeadlineCause(context.Background(), row.ExpiresAt, protocolError(CodeExpired, errors.New("transfer job expired")))
	jobCtx, cancel := context.WithCancelCause(expiryCtx)
	active := &activeJob{ownerID: ownerID, resumeManaged: resumeManaged, ctx: jobCtx, cancel: cancel, stopExpiry: stopExpiry, done: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel(errServiceClosed)
		stopExpiry()
		return JobView{}, ErrServiceClosed
	}
	if s.activeJobs[jobID] != nil {
		s.mu.Unlock()
		cancel(errCanceledByOwner)
		stopExpiry()
		return JobView{}, ErrAlreadyActive
	}
	if s.ownerJobs[ownerID] >= maxActiveJobsPerOwner {
		s.mu.Unlock()
		cancel(errCanceledByOwner)
		stopExpiry()
		return JobView{}, ErrOwnerCapacity
	}
	s.activeJobs[jobID] = active
	s.ownerJobs[ownerID]++
	s.mu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			s.releaseJob(jobID)
		}
	}()

	running, err := s.transitionJobToRunning(ctx, row)
	if err != nil {
		return JobView{}, err
	}
	s.publishJobProgress(ownerID, jobID, TransferEventPayload{JobID: jobID, Status: string(running.Status), ReceivedBytes: running.ReceivedBytes, TotalBytes: running.TotalBytes})

	s.mu.Lock()
	s.wg.Go(func() {
		defer close(active.done)
		s.executeJob(jobCtx, jobID)
	})
	pending = false
	s.pending--
	s.pendingCond.Broadcast()
	s.mu.Unlock()
	reserved = false
	return jobView(running), nil
}

func (s *Service) transitionJobToRunning(ctx context.Context, row *ent.TransferJob) (_ *ent.TransferJob, retErr error) {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	current, err := tx.TransferJob.Query().Where(
		transferjob.IDEQ(row.ID), transferjob.UserIDEQ(row.UserID),
		transferjob.StatusIn(transferjob.StatusReady, transferjob.StatusInterrupted, transferjob.StatusFailed, transferjob.StatusCanceled),
		transferjob.ExpiresAtGT(time.Now().UTC()),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	update := current.Update().
		Where(transferjob.UserIDEQ(row.UserID), transferjob.StatusIn(transferjob.StatusReady, transferjob.StatusInterrupted, transferjob.StatusFailed, transferjob.StatusCanceled)).
		SetStatus(transferjob.StatusRunning).
		ClearFinishedAt().
		ClearErrorCode()
	if current.StartedAt == nil {
		update.SetStartedAt(now)
	}
	running, err := update.Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	items, err := tx.TransferItem.Query().Where(transferitem.UserIDEQ(row.UserID), transferitem.JobIDEQ(row.ID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Status == transferitem.StatusCompleted {
			continue
		}
		if !itemStatusStartable(item.Status) {
			return nil, ErrInvalidState
		}
		itemUpdate := item.Update().
			Where(transferitem.UserIDEQ(row.UserID), transferitem.JobIDEQ(row.ID), transferitem.StatusIn(transferitem.StatusReady, transferitem.StatusInterrupted, transferitem.StatusFailed, transferitem.StatusCanceled)).
			SetStatus(transferitem.StatusRunning).
			ClearFinishedAt().
			ClearErrorCode()
		if item.StartedAt == nil {
			itemUpdate.SetStartedAt(now)
		}
		if _, err := itemUpdate.Save(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), row.UserID, "transfer.start", "job", row.ID, "success"); err != nil {
		return nil, err
	}
	if err := s.commitLifecycle(tx, "job.start"); err != nil {
		return nil, err
	}
	committed = true
	return running, nil
}

func itemStatusStartable(status transferitem.Status) bool {
	return slices.Contains([]transferitem.Status{transferitem.StatusReady, transferitem.StatusInterrupted, transferitem.StatusFailed, transferitem.StatusCanceled}, status)
}

type runnerBlock struct {
	item   *ent.TransferItem
	index  int
	offset int64
	size   int64
	hash   string
}

type blockCompletion struct {
	itemID string
	index  int
	size   int64
}

type itemProgress struct {
	item      *ent.TransferItem
	completed []int
	received  int64
	dirty     bool
}

func (s *Service) executeJob(ctx context.Context, jobID string) {
	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.StatusEQ(transferjob.StatusRunning)).Only(ctx)
	if err != nil {
		s.finishJob(jobID, protocolError(CodeRemoteUnavailable, err))
		return
	}
	items, err := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(row.UserID), transferitem.JobIDEQ(jobID)).Order(ent.Asc(transferitem.FieldCreatedAt), ent.Asc(transferitem.FieldID)).All(ctx)
	if err != nil {
		s.finishJob(jobID, protocolError(CodeRemoteUnavailable, err))
		return
	}
	capability, err := s.box.Open(row.RemoteCapabilityCipher, jobCapabilityAAD(row.UserID, row.ID))
	if err != nil {
		s.finishJob(jobID, protocolError(CodeInvalidCapability, err))
		return
	}
	s.captureSecret("runner.capability", capability)
	defer clearSecret(capability)
	parsed, err := parseCapabilityBytes(capability)
	if err != nil || parsed.shareID != row.RemoteShareID {
		s.finishJob(jobID, protocolError(CodeInvalidCapability, ErrInvalidCapability))
		return
	}
	s.captureSecret("runner.secret", parsed.secret[:])
	defer parsed.clear()
	progress := make(map[string]*itemProgress, len(items))
	tasks := make([]runnerBlock, 0)
	for _, item := range items {
		state := &itemProgress{item: item, completed: slices.Clone(item.CompletedBlocks), received: item.ReceivedBytes}
		progress[item.ID] = state
		complete := make(map[int]struct{}, len(item.CompletedBlocks))
		for _, index := range item.CompletedBlocks {
			complete[index] = struct{}{}
		}
		for index, hash := range item.BlockHashes {
			if _, ok := complete[index]; ok {
				continue
			}
			offset := int64(index) * BlockSize
			size := min(BlockSize, item.SizeBytes-offset)
			tasks = append(tasks, runnerBlock{item: item, index: index, offset: offset, size: size, hash: hash})
		}
	}

	workerCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	taskQueue := make(chan runnerBlock, max(1, len(tasks)))
	for _, task := range tasks {
		taskQueue <- task
	}
	close(taskQueue)
	completions := make(chan blockCompletion, max(1, len(tasks)))
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if s.runnerHooks.workerStarted != nil {
				s.runnerHooks.workerStarted()
			}
			if s.runnerHooks.workerStopped != nil {
				defer s.runnerHooks.workerStopped()
			}
			for task := range taskQueue {
				if context.Cause(workerCtx) != nil {
					return
				}
				if err := s.transferBlock(workerCtx, row, capability, task); err != nil {
					cancel(err)
					return
				}
				select {
				case completions <- blockCompletion{itemID: task.item.ID, index: task.index, size: task.size}:
				case <-workerCtx.Done():
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(completions)
		close(done)
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var totalReceived = row.ReceivedBytes
	for {
		select {
		case completion, ok := <-completions:
			if !ok {
				flushCtx, cancelFlush := context.WithTimeout(context.Background(), 5*time.Second)
				flushErr := s.flushProgress(flushCtx, row, progress, totalReceived)
				cancelFlush()
				if flushErr != nil && context.Cause(workerCtx) == nil {
					cancel(flushErr)
				}
				goto workersFinished
			}
			state := progress[completion.itemID]
			state.completed = append(state.completed, completion.index)
			slices.Sort(state.completed)
			state.received += completion.size
			state.dirty = true
			totalReceived += completion.size
		case <-ticker.C:
			if err := s.flushProgress(workerCtx, row, progress, totalReceived); err != nil {
				cancel(err)
			}
		}
	}

workersFinished:
	<-done
	runErr := context.Cause(workerCtx)
	if runErr == nil {
		for _, item := range items {
			manifest, err := s.storage.BuildFileManifest(ctx, row.UserID, row.ID, item.StorageName, item.ID, item.VirtualPath)
			if err != nil {
				runErr = protocolError(CodeStorageFailed, err)
				break
			}
			if manifest.BLAKE3() != item.Blake3 {
				runErr = protocolError(CodeIntegrityMismatch, errors.New("whole-file BLAKE3 mismatch"))
				break
			}
		}
	}
	s.finishJob(jobID, runErr)
}

func (s *Service) transferBlock(ctx context.Context, job *ent.TransferJob, capability []byte, task runnerBlock) error {
	data, err := fetchRangeSecret(ctx, func(dialCtx context.Context) (net.Conn, error) {
		return s.dialer.DialPort(dialCtx, job.UserID, job.ClientID, ReservedPort)
	}, job.RemoteShareID, capability, task.item.RemoteFileID, task.offset, task.size)
	if err != nil {
		return err
	}
	hash := blake3.Sum256(data)
	if hex.EncodeToString(hash[:]) != task.hash {
		return protocolError(CodeIntegrityMismatch, errors.New("transfer block BLAKE3 mismatch"))
	}
	partial, err := s.storage.OpenPartial(ctx, job.UserID, job.ID, task.item.StorageName, task.item.SizeBytes)
	if err != nil {
		return protocolError(CodeStorageFailed, err)
	}
	if _, err := partial.WriteAt(data, task.offset); err != nil {
		return errors.Join(protocolError(CodeStorageFailed, err), partial.Close())
	}
	if err := partial.Sync(); err != nil {
		return errors.Join(protocolError(CodeStorageFailed, err), partial.Close())
	}
	if s.runnerHooks.afterBlockSync != nil {
		s.runnerHooks.afterBlockSync(task.item.ID, task.index)
	}
	if err := partial.Close(); err != nil {
		return protocolError(CodeStorageFailed, err)
	}
	return nil
}

func (s *Service) flushProgress(ctx context.Context, job *ent.TransferJob, progress map[string]*itemProgress, totalReceived int64) (retErr error) {
	dirty := false
	for _, state := range progress {
		if state.dirty {
			dirty = true
			break
		}
	}
	if !dirty {
		return nil
	}
	s.metadataMu.Lock()
	metadataLocked := true
	defer func() {
		if metadataLocked {
			s.metadataMu.Unlock()
		}
	}()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	jobRow, err := tx.TransferJob.Query().Where(transferjob.IDEQ(job.ID), transferjob.UserIDEQ(job.UserID), transferjob.StatusEQ(transferjob.StatusRunning)).Only(ctx)
	if err != nil {
		return err
	}
	for _, state := range progress {
		if !state.dirty {
			continue
		}
		if s.runnerHooks.beforeProgressSave != nil {
			s.runnerHooks.beforeProgressSave(state.item.ID, len(state.completed))
		}
		itemRow, err := tx.TransferItem.Query().Where(transferitem.IDEQ(state.item.ID), transferitem.UserIDEQ(job.UserID), transferitem.JobIDEQ(job.ID), transferitem.StatusEQ(transferitem.StatusRunning)).Only(ctx)
		if err != nil {
			return err
		}
		if _, err := itemRow.Update().
			Where(transferitem.UserIDEQ(job.UserID), transferitem.JobIDEQ(job.ID), transferitem.StatusEQ(transferitem.StatusRunning)).
			SetCompletedBlocks(slices.Clone(state.completed)).
			SetReceivedBytes(state.received).
			Save(ctx); err != nil {
			return err
		}
	}
	if _, err := jobRow.Update().
		Where(transferjob.UserIDEQ(job.UserID), transferjob.StatusEQ(transferjob.StatusRunning)).
		SetReceivedBytes(totalReceived).
		Save(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	metadataLocked = false
	s.metadataMu.Unlock()
	completedFiles := 0
	lastItemID := ""
	for _, state := range progress {
		if state.received == state.item.SizeBytes {
			completedFiles++
		}
		if state.dirty {
			lastItemID = state.item.ID
		}
		state.dirty = false
	}
	s.publishJobProgress(job.UserID, job.ID, TransferEventPayload{
		JobID: job.ID, ItemID: lastItemID, Status: string(transferjob.StatusRunning),
		ReceivedBytes: totalReceived, TotalBytes: job.TotalBytes, CompletedFiles: completedFiles, TotalFiles: len(progress),
	})
	return nil
}

func (s *Service) finishJob(jobID string, runErr error) {
	s.mu.Lock()
	active := s.activeJobs[jobID]
	s.mu.Unlock()
	if active == nil {
		return
	}
	status, itemStatus, phase, code, _ := terminalJobOutcome(context.Cause(active.ctx), runErr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updated, _, err := s.persistJobTerminalWithRetry(ctx, active.ownerID, jobID, status, itemStatus, code)
	if err != nil {
		s.recordFailure(fmt.Errorf("persist transfer terminal state for %s: %w", jobID, err))
		s.logger.ErrorContext(ctx, "Persist transfer terminal state failed", "job_id", jobID, "error", err)
		s.releaseJob(jobID)
		return
	}
	if !updated {
		s.releaseJob(jobID)
		return
	}
	terminal, terminalErr := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(active.ownerID)).Only(ctx)
	totalFiles, countErr := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(active.ownerID), transferitem.JobIDEQ(jobID)).Count(ctx)
	retryManaged := active.resumeManaged && status == transferjob.StatusFailed && code == transferjob.ErrorCodeTransferRemoteUnavailable
	s.releaseJob(jobID)
	if retryManaged {
		s.requeueManagedResume(active.ownerID, jobID)
	}
	if terminalErr != nil {
		s.logger.ErrorContext(ctx, "Load terminal transfer progress failed", "job_id", jobID, "error", terminalErr)
		return
	}
	if countErr != nil {
		s.logger.ErrorContext(ctx, "Count terminal transfer items failed", "job_id", jobID, "error", countErr)
	}
	completedFiles := 0
	if status == transferjob.StatusCompleted {
		completedFiles = totalFiles
	}
	s.publishTransfer(active.ownerID, jobID, phase, TransferEventPayload{
		JobID: jobID, Status: string(status), ReceivedBytes: terminal.ReceivedBytes, TotalBytes: terminal.TotalBytes,
		CompletedFiles: completedFiles, TotalFiles: totalFiles, ErrorCode: ErrorCode(code),
	})
}

func (s *Service) persistJobTerminalWithRetry(ctx context.Context, ownerID, jobID string, status transferjob.Status, itemStatus transferitem.Status, code transferjob.ErrorCode) (bool, int64, error) {
	var lastErr error
	for attempt := range lifecyclePersistAttempts {
		updated, totalBytes, err := s.persistJobTerminal(ctx, ownerID, jobID, status, itemStatus, code)
		if err == nil {
			return updated, totalBytes, nil
		}
		lastErr = err
		if attempt == lifecyclePersistAttempts-1 {
			break
		}
		timer := time.NewTimer(lifecyclePersistRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, 0, errors.Join(lastErr, ctx.Err())
		}
	}
	return false, 0, fmt.Errorf("persist transfer terminal state after %d attempts: %w", lifecyclePersistAttempts, lastErr)
}

func (s *Service) persistJobTerminal(ctx context.Context, ownerID, jobID string, status transferjob.Status, itemStatus transferitem.Status, code transferjob.ErrorCode) (_ bool, totalBytes int64, retErr error) {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return false, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	job, err := tx.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(transferjob.StatusRunning)).Only(ctx)
	if ent.IsNotFound(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	items, err := tx.TransferItem.Query().Where(transferitem.UserIDEQ(ownerID), transferitem.JobIDEQ(jobID)).All(ctx)
	if err != nil {
		return false, 0, err
	}
	now := time.Now().UTC()
	for _, item := range items {
		if item.Status == transferitem.StatusCompleted {
			continue
		}
		if item.Status != transferitem.StatusRunning {
			return false, 0, ErrInvalidState
		}
		update := item.Update().
			Where(transferitem.UserIDEQ(ownerID), transferitem.JobIDEQ(jobID), transferitem.StatusEQ(transferitem.StatusRunning)).
			SetStatus(itemStatus).
			SetFinishedAt(now)
		if code == "" {
			update.ClearErrorCode()
		} else {
			update.SetErrorCode(transferitem.ErrorCode(code))
		}
		if _, err := update.Save(ctx); err != nil {
			return false, 0, err
		}
	}
	jobUpdate := job.Update().
		Where(transferjob.UserIDEQ(ownerID), transferjob.StatusEQ(transferjob.StatusRunning)).
		SetStatus(status).
		SetFinishedAt(now)
	if status == transferjob.StatusCompleted {
		jobUpdate.SetReceivedBytes(job.TotalBytes).ClearErrorCode()
	} else if code != "" {
		jobUpdate.SetErrorCode(code)
	}
	if _, err := jobUpdate.Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	auditAction, auditOutcome := terminalAuditForJob(status)
	if auditAction == "" {
		return false, 0, ErrInvalidState
	}
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, auditAction, "job", jobID, auditOutcome); err != nil {
		return false, 0, err
	}
	if err := s.commitLifecycle(tx, "job.terminal"); err != nil {
		return false, 0, err
	}
	committed = true
	if status == transferjob.StatusCompleted {
		return true, job.TotalBytes, nil
	}
	return true, job.ReceivedBytes, nil
}

func terminalJobOutcome(cause, runErr error) (transferjob.Status, transferitem.Status, events.RuntimePhase, transferjob.ErrorCode, string) {
	if errors.Is(cause, errServiceClosed) {
		return transferjob.StatusInterrupted, transferitem.StatusInterrupted, events.RuntimePhaseInterrupted, transferjob.ErrorCodeTransferRemoteUnavailable, "transfer.interrupt"
	}
	if errors.Is(cause, errCanceledByOwner) {
		return transferjob.StatusCanceled, transferitem.StatusCanceled, events.RuntimePhaseStopped, transferjob.ErrorCodeTransferCanceled, "transfer.cancel"
	}
	if protocolErr, ok := errors.AsType[*ProtocolError](cause); ok && protocolErr.Code == CodeExpired {
		return transferjob.StatusExpired, transferitem.StatusExpired, events.RuntimePhaseStopped, transferjob.ErrorCodeTransferExpired, "transfer.expire"
	}
	if runErr == nil {
		return transferjob.StatusCompleted, transferitem.StatusCompleted, events.RuntimePhaseReady, "", "transfer.complete"
	}
	code := transferjob.ErrorCodeTransferRemoteUnavailable
	if protocolErr, ok := errors.AsType[*ProtocolError](runErr); ok {
		code = transferjob.ErrorCode(protocolErr.Code)
	}
	return transferjob.StatusFailed, transferitem.StatusFailed, events.RuntimePhaseError, code, "transfer.fail"
}

func (s *Service) CancelJob(ctx context.Context, ownerID, jobID string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	row, err := s.db.TransferJob.Query().Where(transferjob.IDEQ(jobID), transferjob.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Status != transferjob.StatusRunning {
		return ErrInvalidState
	}
	s.mu.Lock()
	active := s.activeJobs[jobID]
	s.mu.Unlock()
	if active == nil || active.ownerID != ownerID {
		return ErrInvalidState
	}
	active.cancel(errCanceledByOwner)
	return nil
}

func (s *Service) RetryJob(ctx context.Context, ownerID, jobID string) (JobView, error) {
	return s.StartJob(ctx, ownerID, jobID)
}

func (s *Service) ResumeJob(ctx context.Context, ownerID, jobID string) (JobView, error) {
	return s.StartJob(ctx, ownerID, jobID)
}

func (s *Service) leavePending() {
	s.mu.Lock()
	s.pending--
	s.pendingCond.Broadcast()
	s.mu.Unlock()
}

func (s *Service) releaseJob(jobID string) {
	s.mu.Lock()
	active := s.activeJobs[jobID]
	if active == nil {
		s.mu.Unlock()
		return
	}
	active.cancel(nil)
	active.stopExpiry()
	delete(s.activeJobs, jobID)
	delete(s.progressPublished, jobID)
	s.ownerJobs[active.ownerID]--
	if s.ownerJobs[active.ownerID] == 0 {
		delete(s.ownerJobs, active.ownerID)
	}
	s.scheduleQueuedResumesLocked(active.ownerID)
	s.mu.Unlock()
}

func (s *Service) enqueueResume(ownerID, jobID string) {
	s.mu.Lock()
	if !slices.ContainsFunc(s.resumeQueue[ownerID], func(queued *queuedResume) bool { return queued.jobID == jobID }) {
		s.resumeQueue[ownerID] = append(s.resumeQueue[ownerID], &queuedResume{jobID: jobID})
	}
	s.scheduleQueuedResumesLocked(ownerID)
	s.mu.Unlock()
	s.wakeResumeQueue()
}

func (s *Service) scheduleQueuedResumesLocked(ownerID string) {
	if s.closed || s.resumeScheduling[ownerID] || len(s.resumeQueue[ownerID]) == 0 || s.ownerJobs[ownerID] >= maxActiveJobsPerOwner {
		return
	}
	s.resumeScheduling[ownerID] = true
	s.wg.Go(func() { s.runQueuedResumes(ownerID) })
}

func (s *Service) runQueuedResumes(ownerID string) {
	for {
		s.mu.Lock()
		if s.closed || context.Cause(s.queueCtx) != nil || len(s.resumeQueue[ownerID]) == 0 || s.ownerJobs[ownerID] >= maxActiveJobsPerOwner {
			delete(s.resumeScheduling, ownerID)
			s.mu.Unlock()
			return
		}
		queued, wait := nextQueuedResume(s.resumeQueue[ownerID], time.Now())
		s.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-s.queueWake:
				if !timer.Stop() {
					<-timer.C
				}
			case <-s.queueCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
			}
			continue
		}

		if _, err := s.startJob(s.queueCtx, ownerID, queued.jobID, true); err != nil {
			if errors.Is(err, ErrOwnerCapacity) {
				s.mu.Lock()
				delete(s.resumeScheduling, ownerID)
				s.mu.Unlock()
				return
			}
			if errors.Is(err, ErrServiceClosed) || errors.Is(err, context.Canceled) {
				return
			}
			s.mu.Lock()
			if retryableResumeError(err) {
				queued.failures++
				queued.nextAttempt = time.Now().Add(resumeRetryDelay(queued.failures))
				moveQueuedResumeToBack(s.resumeQueue[ownerID], queued)
			} else {
				removeQueuedResumeLocked(s, ownerID, queued)
			}
			s.mu.Unlock()
			s.logger.Error("Resume queued transfer failed", "owner_id", ownerID, "job_id", queued.jobID, "error", err)
			continue
		}
		s.mu.Lock()
		if queued.retryRequested {
			queued.retryRequested = false
			queued.failures++
			queued.nextAttempt = time.Now().Add(resumeRetryDelay(queued.failures))
			moveQueuedResumeToBack(s.resumeQueue[ownerID], queued)
		} else {
			removeQueuedResumeLocked(s, ownerID, queued)
		}
		s.mu.Unlock()
	}
}

func (s *Service) requeueManagedResume(ownerID, jobID string) {
	s.mu.Lock()
	queue := s.resumeQueue[ownerID]
	if index := slices.IndexFunc(queue, func(queued *queuedResume) bool { return queued.jobID == jobID }); index >= 0 {
		queue[index].retryRequested = true
	} else {
		s.resumeQueue[ownerID] = append(queue, &queuedResume{jobID: jobID, failures: 1, nextAttempt: time.Now().Add(resumeRetryDelay(1))})
	}
	s.scheduleQueuedResumesLocked(ownerID)
	s.mu.Unlock()
	s.wakeResumeQueue()
}

func nextQueuedResume(queue []*queuedResume, now time.Time) (*queuedResume, time.Duration) {
	var earliest *queuedResume
	for _, queued := range queue {
		if queued.nextAttempt.IsZero() || !queued.nextAttempt.After(now) {
			return queued, 0
		}
		if earliest == nil || queued.nextAttempt.Before(earliest.nextAttempt) {
			earliest = queued
		}
	}
	return earliest, time.Until(earliest.nextAttempt)
}

func moveQueuedResumeToBack(queue []*queuedResume, target *queuedResume) {
	index := slices.Index(queue, target)
	if index < 0 || index == len(queue)-1 {
		return
	}
	copy(queue[index:], queue[index+1:])
	queue[len(queue)-1] = target
}

func removeQueuedResumeLocked(s *Service, ownerID string, target *queuedResume) {
	queue := s.resumeQueue[ownerID]
	index := slices.Index(queue, target)
	if index < 0 {
		return
	}
	queue = append(queue[:index], queue[index+1:]...)
	if len(queue) == 0 {
		delete(s.resumeQueue, ownerID)
		return
	}
	s.resumeQueue[ownerID] = queue
}

func retryableResumeError(err error) bool {
	return !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidState)
}

func resumeRetryDelay(failures int) time.Duration {
	shift := min(max(failures-1, 0), 6)
	return min(time.Second, 10*time.Millisecond*time.Duration(1<<shift))
}

func (s *Service) wakeResumeQueue() {
	select {
	case s.queueWake <- struct{}{}:
	default:
	}
}

func jobView(row *ent.TransferJob) JobView {
	return JobView{
		ID: row.ID, ClientID: row.ClientID, RemoteShareID: row.RemoteShareID, Status: string(row.Status),
		TotalBytes: row.TotalBytes, ReceivedBytes: row.ReceivedBytes, ExpiresAt: row.ExpiresAt.UTC(), ErrorCode: ErrorCode(row.ErrorCode),
	}
}
