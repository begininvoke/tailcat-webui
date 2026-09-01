package transfer

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
	"uuid"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
	"github.com/ca-x/tailcat-webui/ent/tailserver"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/events"
	"github.com/ca-x/tailcat-webui/internal/secrets"
)

const (
	defaultTransferExpiry = 24 * time.Hour
	maxActiveJobsPerOwner = 2
)

var (
	ErrNotFound        = errors.New("transfer resource not found")
	ErrInvalidState    = errors.New("invalid transfer state transition")
	ErrOwnerCapacity   = errors.New("transfer owner capacity reached")
	ErrAlreadyActive   = errors.New("transfer job is already active")
	ErrServiceClosed   = errors.New("transfer service is closed")
	errCanceledByOwner = errors.New("transfer canceled by owner")
	errServiceClosed   = errors.New("transfer service shutdown")
)

type ClientDialer interface {
	DialPort(context.Context, string, string, uint16) (net.Conn, error)
}

type AuditRecorder interface {
	Record(context.Context, audit.Entry) error
}

type EventPublisher interface {
	PublishEvent(string, events.Envelope)
}

type CreateShareInput struct {
	ServerID  string
	ExpiresAt time.Time
}

type ShareView struct {
	ID         string    `json:"id"`
	ServerID   string    `json:"server_id"`
	Status     string    `json:"status"`
	TotalBytes int64     `json:"total_bytes"`
	FileCount  int       `json:"file_count"`
	ExpiresAt  time.Time `json:"expires_at"`
	Capability string    `json:"capability,omitempty"`
}

type StageFileInput struct {
	VirtualPath string
	Size        int64
	Body        io.ReadCloser
}

type FileView struct {
	ID          string    `json:"id"`
	VirtualPath string    `json:"virtual_path"`
	Size        int64     `json:"size"`
	MTime       time.Time `json:"mtime"`
}

func (s *Service) ListShares(ctx context.Context, ownerID string) ([]ShareView, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := s.db.TransferShare.Query().Where(transfershare.UserIDEQ(ownerID)).Order(ent.Desc(transfershare.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transfer shares: %w", err)
	}
	views := make([]ShareView, len(rows))
	for index, row := range rows {
		views[index] = shareView(row)
	}
	return views, nil
}

func (s *Service) ListShareFiles(ctx context.Context, ownerID, shareID string) ([]FileView, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if exists, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Exist(ctx); err != nil {
		return nil, err
	} else if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.ShareFile.Query().Where(sharefile.UserIDEQ(ownerID), sharefile.ShareIDEQ(shareID)).Order(ent.Asc(sharefile.FieldCreatedAt), ent.Asc(sharefile.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transfer share files: %w", err)
	}
	views := make([]FileView, len(rows))
	for index, row := range rows {
		views[index] = FileView{ID: row.ID, VirtualPath: row.VirtualPath, Size: row.SizeBytes, MTime: row.Mtime.UTC()}
	}
	return views, nil
}

type TransferEventPayload struct {
	ShareID        string    `json:"share_id,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	ItemID         string    `json:"item_id,omitempty"`
	Status         string    `json:"status"`
	ReceivedBytes  int64     `json:"received_bytes,omitzero"`
	TotalBytes     int64     `json:"total_bytes,omitzero"`
	CompletedFiles int       `json:"completed_files,omitzero"`
	TotalFiles     int       `json:"total_files,omitzero"`
	ErrorCode      ErrorCode `json:"error_code,omitempty"`
}

type activeStream struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type activeJob struct {
	ownerID    string
	ctx        context.Context
	cancel     context.CancelCauseFunc
	stopExpiry context.CancelFunc
	done       chan struct{}
}

type runnerHooks struct {
	workerStarted      func()
	workerStopped      func()
	afterBlockSync     func(string, int)
	beforeProgressSave func(string, int)
}

type Service struct {
	db        *ent.Client
	storage   *Storage
	box       *secrets.Box
	dialer    ClientDialer
	auditor   AuditRecorder
	publisher EventPublisher
	logger    *slog.Logger

	compareCapability func([]byte, []byte) int

	metadataMu       sync.Mutex
	mu               sync.Mutex
	pendingCond      *sync.Cond
	closed           bool
	pending          int
	activeJobs       map[string]*activeJob
	ownerJobs        map[string]int
	resumeQueue      map[string][]string
	resumeScheduling map[string]bool
	streams          map[string]map[*activeStream]struct{}
	wg               sync.WaitGroup
	runnerHooks      runnerHooks
	closeOnce        sync.Once
	closeDone        chan struct{}
	closeErr         error
	failures         []error
}

func NewService(ctx context.Context, db *ent.Client, storage *Storage, box *secrets.Box, dialer ClientDialer, auditor AuditRecorder, publisher EventPublisher, logger *slog.Logger) (*Service, error) {
	if db == nil || storage == nil || box == nil || dialer == nil || auditor == nil || publisher == nil || logger == nil {
		return nil, errors.New("transfer service: nil dependency")
	}
	if !box.Available() {
		return nil, secrets.ErrUnavailable
	}
	service := &Service{
		db: db, storage: storage, box: box, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger,
		compareCapability: subtle.ConstantTimeCompare,
		activeJobs:        make(map[string]*activeJob), ownerJobs: make(map[string]int), streams: make(map[string]map[*activeStream]struct{}),
		resumeQueue: make(map[string][]string), resumeScheduling: make(map[string]bool),
		closeDone: make(chan struct{}),
	}
	service.pendingCond = sync.NewCond(&service.mu)
	if err := service.reconcileAudits(ctx); err != nil {
		return nil, err
	}
	if err := service.interruptAbandoned(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) CreateShare(ctx context.Context, ownerID string, input CreateShareInput) (ShareView, error) {
	if err := s.ensureOpen(); err != nil {
		return ShareView{}, err
	}
	if validateEntityID(ownerID) != nil || validateEntityID(input.ServerID) != nil {
		return ShareView{}, ErrNotFound
	}
	if _, err := s.db.TailServer.Query().Where(tailserver.IDEQ(input.ServerID), tailserver.UserIDEQ(ownerID)).Only(ctx); ent.IsNotFound(err) {
		return ShareView{}, ErrNotFound
	} else if err != nil {
		return ShareView{}, fmt.Errorf("validate transfer server ownership: %w", err)
	}
	now := time.Now()
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(defaultTransferExpiry)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(defaultTransferExpiry)) {
		return ShareView{}, fmt.Errorf("%w: share expiry", ErrInvalidState)
	}
	shareID := newEntityID()
	capability, hash, err := newCapability(shareID)
	if err != nil {
		return ShareView{}, err
	}
	row, err := s.db.TransferShare.Create().
		SetID(shareID).
		SetUserID(ownerID).
		SetServerID(input.ServerID).
		SetStatus(transfershare.StatusStaging).
		SetCapabilityHash(hash).
		SetExpiresAt(expiresAt.UTC()).
		Save(ctx)
	if err != nil {
		return ShareView{}, fmt.Errorf("create transfer share: %w", err)
	}
	if err := s.recordLifecycle(ctx, ownerID, "transfer.create", "share", row.ID, "success"); err != nil {
		cleanupErr := s.db.TransferShare.DeleteOneID(row.ID).Exec(context.WithoutCancel(ctx))
		return ShareView{}, errors.Join(err, cleanupErr)
	}
	s.publishTransfer(ownerID, row.ID, events.RuntimePhaseIdle, TransferEventPayload{ShareID: row.ID, Status: string(row.Status)})
	view := shareView(row)
	view.Capability = capability
	return view, nil
}

func (s *Service) StageFile(ctx context.Context, ownerID, shareID string, input StageFileInput) (_ FileView, retErr error) {
	if err := s.ensureOpen(); err != nil {
		return FileView{}, err
	}
	if input.Body == nil || input.Size < 0 || input.Size > MaxFileBytes || validateVirtualPath(input.VirtualPath) != nil {
		if input.Body != nil {
			_ = input.Body.Close()
		}
		return FileView{}, fmt.Errorf("%w: staged file input", ErrInvalidState)
	}
	share, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		_ = input.Body.Close()
		return FileView{}, ErrNotFound
	}
	if err != nil {
		_ = input.Body.Close()
		return FileView{}, fmt.Errorf("load staging transfer share: %w", err)
	}
	if share.Status != transfershare.StatusStaging {
		_ = input.Body.Close()
		return FileView{}, ErrInvalidState
	}
	if !share.ExpiresAt.After(time.Now()) || share.FileCount >= MaxFilesPerShare || input.Size > MaxShareBytes-share.TotalBytes {
		_ = input.Body.Close()
		return FileView{}, fmt.Errorf("%w: share limit or expiry", ErrInvalidState)
	}
	fileID := newEntityID()
	stored, err := s.storage.Store(ctx, ownerID, shareID, input.Size, input.Body)
	if err != nil {
		if stored.StorageName != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			retErr = errors.Join(err, s.storage.Remove(cleanupCtx, ownerID, shareID, stored.StorageName))
			return FileView{}, retErr
		}
		return FileView{}, err
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, s.storage.Remove(cleanupCtx, ownerID, shareID, stored.StorageName))
	}()
	manifest, err := s.storage.BuildFileManifest(ctx, ownerID, shareID, stored.StorageName, fileID, input.VirtualPath)
	if err != nil {
		return FileView{}, err
	}

	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return FileView{}, fmt.Errorf("begin staged-file metadata transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	current, err := tx.TransferShare.Query().Where(
		transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(transfershare.StatusStaging),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return FileView{}, ErrInvalidState
	}
	if err != nil {
		return FileView{}, fmt.Errorf("recheck staging transfer share: %w", err)
	}
	if !current.ExpiresAt.After(time.Now()) || current.FileCount >= MaxFilesPerShare || manifest.Size() > MaxShareBytes-current.TotalBytes {
		return FileView{}, fmt.Errorf("%w: share limit or expiry", ErrInvalidState)
	}
	row, err := tx.ShareFile.Create().
		SetID(fileID).
		SetUserID(ownerID).
		SetShareID(shareID).
		SetStorageName(stored.StorageName).
		SetVirtualPath(manifest.VirtualPath()).
		SetSizeBytes(manifest.Size()).
		SetMtime(manifest.MTime().UTC()).
		SetBlake3(manifest.BLAKE3()).
		SetBlockSize(BlockSize).
		SetBlockHashes(manifest.BlockHashes()).
		Save(ctx)
	if err != nil {
		return FileView{}, fmt.Errorf("create staged-file metadata: %w", err)
	}
	if _, err := current.Update().
		Where(transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(transfershare.StatusStaging)).
		SetTotalBytes(current.TotalBytes + manifest.Size()).
		SetFileCount(current.FileCount + 1).
		Save(ctx); err != nil {
		return FileView{}, fmt.Errorf("update transfer share counters: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FileView{}, fmt.Errorf("commit staged-file metadata: %w", err)
	}
	committed = true
	cleanup = false
	return FileView{ID: row.ID, VirtualPath: row.VirtualPath, Size: row.SizeBytes, MTime: row.Mtime.UTC()}, nil
}

func (s *Service) FinalizeShare(ctx context.Context, ownerID, shareID string) (ShareView, error) {
	if err := s.ensureOpen(); err != nil {
		return ShareView{}, err
	}
	row, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ShareView{}, ErrNotFound
	}
	if err != nil {
		return ShareView{}, fmt.Errorf("load staging share: %w", err)
	}
	if row.Status != transfershare.StatusStaging {
		return ShareView{}, ErrInvalidState
	}
	if row.FileCount == 0 || !row.ExpiresAt.After(time.Now()) {
		return ShareView{}, ErrInvalidState
	}
	now := time.Now().UTC()
	updated, err := row.Update().
		Where(transfershare.UserIDEQ(ownerID), transfershare.StatusEQ(transfershare.StatusStaging), transfershare.ExpiresAtGT(now)).
		SetStatus(transfershare.StatusReady).
		SetReadyAt(now).
		Save(ctx)
	if ent.IsNotFound(err) {
		return ShareView{}, ErrInvalidState
	}
	if err != nil {
		return ShareView{}, fmt.Errorf("finalize transfer share: %w", err)
	}
	s.publishTransfer(ownerID, shareID, events.RuntimePhaseReady, TransferEventPayload{ShareID: shareID, Status: string(updated.Status)})
	return shareView(updated), nil
}

func (s *Service) RotateShare(ctx context.Context, ownerID, shareID string) (string, error) {
	if err := s.ensureOpen(); err != nil {
		return "", err
	}
	row, capability, hash, err := s.rotateCapabilityHash(ctx, ownerID, shareID)
	if err != nil {
		return "", err
	}
	s.cancelShareStreams(shareID, ErrInvalidCapability)
	if err := s.recordLifecycle(ctx, ownerID, "transfer.rotate", "share", shareID, "success"); err != nil {
		s.metadataMu.Lock()
		_, rollbackErr := s.db.TransferShare.Update().Where(
			transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID), transfershare.CapabilityHashEQ(hash),
		).SetCapabilityHash(row.CapabilityHash).Save(context.WithoutCancel(ctx))
		s.metadataMu.Unlock()
		return "", errors.Join(err, rollbackErr)
	}
	return capability, nil
}

func (s *Service) rotateCapabilityHash(ctx context.Context, ownerID, shareID string) (*ent.TransferShare, string, []byte, error) {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	row, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, "", nil, ErrNotFound
	}
	if err != nil {
		return nil, "", nil, fmt.Errorf("load transfer share for rotation: %w", err)
	}
	if row.Status != transfershare.StatusStaging && row.Status != transfershare.StatusReady {
		return nil, "", nil, ErrInvalidState
	}
	now := time.Now().UTC()
	if !row.ExpiresAt.After(now) {
		return nil, "", nil, fmt.Errorf("%w: share expired before rotation", ErrInvalidState)
	}
	capability, hash, err := newCapability(row.ID)
	if err != nil {
		return nil, "", nil, err
	}
	if _, err := row.Update().
		Where(transfershare.UserIDEQ(ownerID), transfershare.StatusIn(transfershare.StatusStaging, transfershare.StatusReady), transfershare.ExpiresAtGT(now)).
		SetCapabilityHash(hash).
		Save(ctx); ent.IsNotFound(err) {
		return nil, "", nil, fmt.Errorf("%w: rotation compare-and-swap lost", ErrInvalidState)
	} else if err != nil {
		return nil, "", nil, fmt.Errorf("rotate transfer capability: %w", err)
	}
	return row, capability, hash, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		var streamDones []<-chan struct{}
		if !s.closed {
			s.closed = true
			for _, job := range s.activeJobs {
				job.cancel(errServiceClosed)
			}
			for _, streams := range s.streams {
				for stream := range streams {
					streamDones = append(streamDones, stream.done)
					stream.cancel(errServiceClosed)
				}
			}
		}
		for s.pending > 0 {
			s.pendingCond.Wait()
		}
		s.mu.Unlock()
		s.wg.Wait()
		for _, done := range streamDones {
			<-done
		}
		s.mu.Lock()
		s.closeErr = errors.Join(s.failures...)
		s.mu.Unlock()
		close(s.closeDone)
	})
	<-s.closeDone
	return s.closeErr
}

func (s *Service) ensureOpen() error {
	if s == nil {
		return ErrServiceClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrServiceClosed
	}
	return nil
}

func newCapability(shareID string) (string, []byte, error) {
	var secret [capabilitySecretBytes]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", nil, fmt.Errorf("generate transfer capability: %w", err)
	}
	return encodeCapability(shareID, secret)
}

func newEntityID() string { return uuid.NewV7().String() }

// jobCapabilityAAD is intentionally versioned and binds both immutable tenant
// and job UUIDs. Neither component may be substituted during resume.
func jobCapabilityAAD(ownerID, jobID string) string {
	return "tailcat-transfer/job-capability/v1/" + ownerID + "/" + jobID
}

func shareView(row *ent.TransferShare) ShareView {
	return ShareView{
		ID: row.ID, ServerID: row.ServerID, Status: string(row.Status), TotalBytes: row.TotalBytes,
		FileCount: row.FileCount, ExpiresAt: row.ExpiresAt.UTC(),
	}
}

func (s *Service) recordLifecycle(ctx context.Context, ownerID, action, resourceKind, resourceID, outcome string) error {
	entry := audit.Entry{
		ID: resourceKind + ":" + resourceID + ":" + action, UserID: ownerID, Action: action,
		ResourceKind: resourceKind, ResourceID: resourceID, Outcome: outcome,
	}
	var lastErr error
	for attempt := range 3 {
		if err := s.auditor.Record(ctx, entry); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("record transfer lifecycle: %w", errors.Join(lastErr, ctx.Err()))
		}
	}
	return fmt.Errorf("record transfer lifecycle after 3 attempts: %w", lastErr)
}

func (s *Service) publishTransfer(ownerID, resourceID string, phase events.RuntimePhase, payload TransferEventPayload) {
	s.publisher.PublishEvent(ownerID, events.Envelope{
		Version: 1, Type: "transfer", ResourceKind: "transfer", ResourceID: resourceID,
		OperationID: resourceID, Phase: phase, Payload: payload,
	})
}

func (s *Service) recordFailure(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.failures = append(s.failures, err)
	s.mu.Unlock()
}
