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
	"strconv"
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
	defaultTransferExpiry      = 24 * time.Hour
	maxActiveJobsPerOwner      = 2
	MaxRetainedSharesPerOwner  = 128
	MaxRetainedJobsPerOwner    = 128
	lifecyclePersistAttempts   = 3
	lifecyclePersistRetryDelay = 10 * time.Millisecond
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
	RecordWithClient(context.Context, *ent.Client, audit.Entry) error
}

type EventPublisher interface {
	PublishEvent(string, events.Envelope)
}

type CreateShareInput struct {
	ServerID  string
	ExpiresAt time.Time
}

type ShareView struct {
	ID         string     `json:"id"`
	ServerID   string     `json:"server_id"`
	Status     string     `json:"status"`
	TotalBytes int64      `json:"total_bytes"`
	FileCount  int        `json:"file_count"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Capability string     `json:"capability,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
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
	CreatedAt   time.Time `json:"created_at"`
}

type ServiceLimits struct {
	MaxFileBytes            int64
	MaxShareBytes           int64
	MaxJobBytes             int64
	MaxFilesPerShare        int
	Workers                 int
	MaxJobsPerOwner         int
	MaxSharesPerOwner       int
	MaxRetainedJobsPerOwner int
	Expiry                  time.Duration
}

func DefaultServiceLimits() ServiceLimits {
	return ServiceLimits{
		MaxFileBytes: MaxFileBytes, MaxShareBytes: MaxShareBytes, MaxJobBytes: MaxShareBytes,
		MaxFilesPerShare: MaxFilesPerShare, Workers: 4, MaxJobsPerOwner: 2,
		MaxSharesPerOwner: MaxRetainedSharesPerOwner, MaxRetainedJobsPerOwner: MaxRetainedJobsPerOwner,
		Expiry: defaultTransferExpiry,
	}
}

func (s *Service) maxJobsPerOwner() int {
	if s.limits.MaxJobsPerOwner == 0 {
		return maxActiveJobsPerOwner
	}
	return s.limits.MaxJobsPerOwner
}

type generatedCapability struct {
	text capabilityText
	hash []byte
}

func (capability *generatedCapability) clear() {
	capability.text.clear()
	capability.hash = nil
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

func (s *Service) Share(ctx context.Context, ownerID, shareID string) (ShareView, error) {
	if err := s.ensureOpen(); err != nil {
		return ShareView{}, err
	}
	row, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ShareView{}, ErrNotFound
	}
	if err != nil {
		return ShareView{}, fmt.Errorf("load transfer share: %w", err)
	}
	return shareView(row), nil
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
		views[index] = fileView(row)
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
	shareID     string
	generation  uint64
	ctx         context.Context
	cancel      context.CancelCauseFunc
	serveCtx    context.Context
	cancelServe context.CancelFunc
	done        chan struct{}
	finishOnce  sync.Once
}

type shareGate struct {
	generation  uint64
	accepting   bool
	provisional map[*activeStream]struct{}
	active      map[*activeStream]struct{}
	expiry      *shareExpiryTask
}

type shareExpiryTask struct {
	expiresAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

type handlerHooks struct {
	afterAuthorized        func()
	afterRevocationClosed  func(string)
	afterExpiryArmed       func(string, func())
	beforeGateExpiryCancel func(string)
}

type lifecycleHooks struct {
	beforeCommit func(string) error
	afterCommit  func(string)
}

type secretHooks struct {
	capture func(string, []byte)
}

type activeJob struct {
	ownerID       string
	resumeManaged bool
	ctx           context.Context
	cancel        context.CancelCauseFunc
	stopExpiry    context.CancelFunc
	done          chan struct{}
}

type runnerHooks struct {
	workerStarted             func()
	workerStopped             func()
	afterBlockSync            func(string, int)
	beforeProgressSave        func(string, int)
	beforeResumeCapacityClear func()
}

type queuedResume struct {
	jobID          string
	failures       int
	nextAttempt    time.Time
	retryRequested bool
}

type Service struct {
	db           *ent.Client
	storage      *Storage
	box          *secrets.Box
	dialer       ClientDialer
	auditor      AuditRecorder
	publisher    EventPublisher
	logger       *slog.Logger
	limits       ServiceLimits
	handlerSlots chan struct{}

	compareCapability func([]byte, []byte) int
	progressNow       func() time.Time
	resumeNow         func() time.Time
	resumeJitter      func(time.Duration) time.Duration

	metadataMu        sync.Mutex
	objectMu          sync.Mutex
	pendingShares     map[string]int
	pendingJobs       map[string]int
	mu                sync.Mutex
	pendingCond       *sync.Cond
	closed            bool
	pending           int
	activeJobs        map[string]*activeJob
	ownerJobs         map[string]int
	resumeQueue       map[string][]*queuedResume
	resumeScheduling  map[string]bool
	resumeFailures    map[string]int
	progressPublished map[string]time.Time
	queueCtx          context.Context
	cancelQueue       context.CancelCauseFunc
	queueWake         chan struct{}
	expiryCtx         context.Context
	cancelExpiry      context.CancelCauseFunc
	expiryWake        chan struct{}
	shareGates        map[string]*shareGate
	shareOps          map[string]*shareOperationLock
	jobReadGates      map[string]*jobReadGate
	wg                sync.WaitGroup
	runnerHooks       runnerHooks
	handlerHooks      handlerHooks
	lifecycleHooks    lifecycleHooks
	secretHooks       secretHooks
	closeOnce         sync.Once
	closeDone         chan struct{}
	closeErr          error
	failures          []error
}

func NewService(ctx context.Context, db *ent.Client, storage *Storage, box *secrets.Box, dialer ClientDialer, auditor AuditRecorder, publisher EventPublisher, logger *slog.Logger) (*Service, error) {
	return NewServiceWithLimits(ctx, db, storage, box, dialer, auditor, publisher, logger, DefaultServiceLimits())
}

func NewServiceWithLimits(ctx context.Context, db *ent.Client, storage *Storage, box *secrets.Box, dialer ClientDialer, auditor AuditRecorder, publisher EventPublisher, logger *slog.Logger, limits ServiceLimits) (*Service, error) {
	if db == nil || storage == nil || box == nil || dialer == nil || auditor == nil || publisher == nil || logger == nil {
		return nil, errors.New("transfer service: nil dependency")
	}
	if limits.MaxSharesPerOwner == 0 {
		limits.MaxSharesPerOwner = MaxRetainedSharesPerOwner
	}
	if limits.MaxRetainedJobsPerOwner == 0 {
		limits.MaxRetainedJobsPerOwner = MaxRetainedJobsPerOwner
	}
	if limits.MaxFileBytes <= 0 || limits.MaxFileBytes > MaxFileBytes || limits.MaxShareBytes < limits.MaxFileBytes || limits.MaxShareBytes > MaxShareBytes || limits.MaxJobBytes < limits.MaxFileBytes || limits.MaxJobBytes > MaxShareBytes || limits.MaxFilesPerShare <= 0 || limits.MaxFilesPerShare > MaxFilesPerShare || limits.Workers != 4 || limits.MaxJobsPerOwner <= 0 || limits.MaxJobsPerOwner > 2 || limits.MaxSharesPerOwner <= 0 || limits.MaxSharesPerOwner > MaxRetainedSharesPerOwner || limits.MaxRetainedJobsPerOwner <= 0 || limits.MaxRetainedJobsPerOwner > MaxRetainedJobsPerOwner || limits.Expiry <= 0 || limits.Expiry > defaultTransferExpiry {
		return nil, errors.New("transfer service: invalid limits")
	}
	if !box.Available() {
		return nil, secrets.ErrUnavailable
	}
	queueCtx, cancelQueue := context.WithCancelCause(context.Background())
	expiryCtx, cancelExpiry := context.WithCancelCause(context.Background())
	service := &Service{
		db: db, storage: storage, box: box, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger, limits: limits,
		compareCapability: subtle.ConstantTimeCompare,
		progressNow:       time.Now,
		resumeNow:         time.Now,
		resumeJitter:      jitterResumeDelay,
		handlerSlots:      make(chan struct{}, 16),
		activeJobs:        make(map[string]*activeJob), ownerJobs: make(map[string]int), shareGates: make(map[string]*shareGate), shareOps: make(map[string]*shareOperationLock), jobReadGates: make(map[string]*jobReadGate),
		resumeQueue: make(map[string][]*queuedResume), resumeScheduling: make(map[string]bool), resumeFailures: make(map[string]int),
		progressPublished: make(map[string]time.Time),
		pendingShares:     make(map[string]int), pendingJobs: make(map[string]int),
		queueCtx: queueCtx, cancelQueue: cancelQueue, queueWake: make(chan struct{}, 1),
		expiryCtx: expiryCtx, cancelExpiry: cancelExpiry, expiryWake: make(chan struct{}, 1),
		closeDone: make(chan struct{}),
	}
	service.pendingCond = sync.NewCond(&service.mu)
	if err := service.reconcileAudits(ctx); err != nil {
		return nil, err
	}
	if err := service.interruptAbandoned(ctx); err != nil {
		return nil, err
	}
	service.wg.Go(service.runExpiryScheduler)
	service.wakeExpiryScheduler()
	return service, nil
}

func (s *Service) CreateShare(ctx context.Context, ownerID string, input CreateShareInput) (_ ShareView, retErr error) {
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
	releaseObject, err := s.reserveOwnerObject(ctx, ownerID, ownerObjectShare)
	if err != nil {
		return ShareView{}, err
	}
	defer releaseObject()
	now := time.Now()
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(s.limits.Expiry)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(s.limits.Expiry)) {
		return ShareView{}, fmt.Errorf("%w: share expiry", ErrInvalidState)
	}
	shareID := newEntityID()
	capability, err := s.generateCapability(shareID)
	if err != nil {
		return ShareView{}, err
	}
	defer capability.clear()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return ShareView{}, fmt.Errorf("begin transfer-share create transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	row, err := tx.Client().TransferShare.Create().
		SetID(shareID).
		SetUserID(ownerID).
		SetServerID(input.ServerID).
		SetStatus(transfershare.StatusStaging).
		SetCapabilityHash(capability.hash).
		SetExpiresAt(expiresAt.UTC()).
		Save(ctx)
	if err != nil {
		return ShareView{}, fmt.Errorf("create transfer share: %w", err)
	}
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, "transfer.create", "share", row.ID, "success"); err != nil {
		return ShareView{}, err
	}
	if err := s.commitLifecycle(tx, "share.create"); err != nil {
		return ShareView{}, fmt.Errorf("commit transfer-share create: %w", err)
	}
	committed = true
	s.wakeExpiryScheduler()
	s.publishTransfer(ownerID, row.ID, events.RuntimePhaseIdle, TransferEventPayload{ShareID: row.ID, Status: string(row.Status)})
	view := shareView(row)
	view.Capability = string(capability.text)
	return view, nil
}

func (s *Service) StageFile(ctx context.Context, ownerID, shareID string, input StageFileInput) (_ FileView, retErr error) {
	sourceOwned := input.Body != nil
	defer func() {
		if sourceOwned {
			retErr = errors.Join(retErr, input.Body.Close())
		}
	}()
	if err := s.ensureOpen(); err != nil {
		return FileView{}, err
	}
	if input.Body == nil || input.Size < 0 || input.Size > s.limits.MaxFileBytes || validateVirtualPath(input.VirtualPath) != nil {
		return FileView{}, fmt.Errorf("%w: staged file input", ErrInvalidState)
	}
	share, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return FileView{}, ErrNotFound
	}
	if err != nil {
		return FileView{}, fmt.Errorf("load staging transfer share: %w", err)
	}
	if share.Status != transfershare.StatusStaging {
		return FileView{}, ErrInvalidState
	}
	if !share.ExpiresAt.After(time.Now()) || share.FileCount >= s.limits.MaxFilesPerShare || input.Size > s.limits.MaxShareBytes-share.TotalBytes {
		return FileView{}, fmt.Errorf("%w: share limit or expiry", ErrInvalidState)
	}
	fileID := newEntityID()
	sourceOwned = false
	stored, err := s.storage.StoreScoped(ctx, ownerID, shareID, input.Size, ScopeLimits{MaxBytes: s.limits.MaxShareBytes, MaxFiles: s.limits.MaxFilesPerShare}, input.Body)
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
	if !current.ExpiresAt.After(time.Now()) || current.FileCount >= s.limits.MaxFilesPerShare || manifest.Size() > s.limits.MaxShareBytes-current.TotalBytes {
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
	return fileView(row), nil
}

func (s *Service) FinalizeShare(ctx context.Context, ownerID, shareID string) (ShareView, error) {
	if err := s.ensureOpen(); err != nil {
		return ShareView{}, err
	}
	unlock := s.lockShareOperation(shareID)
	defer unlock()
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return ShareView{}, fmt.Errorf("begin transfer share finalize: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	row, err := tx.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
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
	if err := s.recordLifecycleWithClient(ctx, tx.Client(), ownerID, "transfer.finalize", "share", shareID, "success"); err != nil {
		return ShareView{}, err
	}
	if err := s.commitLifecycle(tx, "share.finalize"); err != nil {
		return ShareView{}, fmt.Errorf("commit transfer share finalize: %w", err)
	}
	committed = true
	s.publishTransfer(ownerID, shareID, events.RuntimePhaseReady, TransferEventPayload{ShareID: shareID, Status: string(updated.Status)})
	return shareView(updated), nil
}

func (s *Service) RotateShare(ctx context.Context, ownerID, shareID string) (string, error) {
	if err := s.ensureOpen(); err != nil {
		return "", err
	}
	if _, err := s.db.TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx); ent.IsNotFound(err) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("load transfer share for rotation: %w", err)
	}
	unlock := s.lockShareOperation(shareID)
	defer unlock()
	generation, err := s.closeShareAdmission(ctx, shareID, ErrInvalidCapability)
	if err != nil {
		return "", err
	}
	capability, err := s.rotateCapability(ctx, ownerID, shareID)
	if err != nil {
		s.reopenShareAdmissionIfLegal(context.WithoutCancel(ctx), ownerID, shareID, generation)
		return "", err
	}
	defer capability.clear()
	if !s.reopenShareAdmission(shareID, generation) {
		return "", fmt.Errorf("%w: capability rotated but a later revocation kept admission closed", ErrInvalidState)
	}
	return string(capability.text), nil
}

func (s *Service) reopenShareAdmissionIfLegal(ctx context.Context, ownerID, shareID string, generation uint64) {
	exists, err := s.db.TransferShare.Query().Where(
		transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID),
		transfershare.StatusIn(transfershare.StatusStaging, transfershare.StatusReady),
		transfershare.ExpiresAtGT(time.Now().UTC()),
	).Exist(ctx)
	if err == nil && exists {
		s.reopenShareAdmission(shareID, generation)
	}
}

func (s *Service) rotateCapability(ctx context.Context, ownerID, shareID string) (_ generatedCapability, retErr error) {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return generatedCapability{}, fmt.Errorf("begin capability rotation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()
	row, err := tx.Client().TransferShare.Query().Where(transfershare.IDEQ(shareID), transfershare.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return generatedCapability{}, ErrNotFound
	}
	if err != nil {
		return generatedCapability{}, fmt.Errorf("load transfer share for rotation: %w", err)
	}
	if row.Status != transfershare.StatusStaging && row.Status != transfershare.StatusReady {
		return generatedCapability{}, ErrInvalidState
	}
	now := time.Now().UTC()
	if !row.ExpiresAt.After(now) {
		return generatedCapability{}, fmt.Errorf("%w: share expired before rotation", ErrInvalidState)
	}
	capability, err := s.generateCapability(row.ID)
	if err != nil {
		return generatedCapability{}, err
	}
	owned := true
	defer func() {
		if owned {
			capability.clear()
		}
	}()
	nextGeneration := row.CapabilityGeneration + 1
	if _, err := row.Update().
		Where(transfershare.UserIDEQ(ownerID), transfershare.StatusIn(transfershare.StatusStaging, transfershare.StatusReady), transfershare.ExpiresAtGT(now)).
		SetCapabilityHash(capability.hash).
		SetCapabilityGeneration(nextGeneration).
		Save(ctx); ent.IsNotFound(err) {
		return generatedCapability{}, fmt.Errorf("%w: rotation compare-and-swap lost", ErrInvalidState)
	} else if err != nil {
		return generatedCapability{}, fmt.Errorf("rotate transfer capability: %w", err)
	}
	if err := s.recordLifecycleOccurrenceWithClient(ctx, tx.Client(), ownerID, "transfer.rotate", "share", shareID, "success", nextGeneration-1); err != nil {
		return generatedCapability{}, err
	}
	if err := s.commitLifecycle(tx, "share.rotate"); err != nil {
		return generatedCapability{}, fmt.Errorf("commit capability rotation: %w", err)
	}
	committed = true
	owned = false
	return capability, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		var streamDones []<-chan struct{}
		var streams []*activeStream
		var expiryDones []<-chan struct{}
		var jobCancels []context.CancelCauseFunc
		var readLeases []*jobReadLease
		if !s.closed {
			s.closed = true
			for _, job := range s.activeJobs {
				jobCancels = append(jobCancels, job.cancel)
			}
			for _, gate := range s.shareGates {
				gate.accepting = false
				gate.generation++
				for stream := range gate.provisional {
					streams = append(streams, stream)
					streamDones = append(streamDones, stream.done)
				}
				for stream := range gate.active {
					streams = append(streams, stream)
					streamDones = append(streamDones, stream.done)
				}
				if gate.expiry != nil {
					gate.expiry.cancel()
					expiryDones = append(expiryDones, gate.expiry.done)
				}
			}
			for _, gate := range s.jobReadGates {
				gate.accepting = false
				gate.generation++
				for lease := range gate.leases {
					readLeases = append(readLeases, lease)
				}
			}
		}
		for s.pending > 0 {
			s.pendingCond.Wait()
		}
		s.mu.Unlock()
		for _, cancel := range jobCancels {
			cancel(errServiceClosed)
		}
		s.cancelQueue(errServiceClosed)
		s.cancelExpiry(errServiceClosed)
		for _, stream := range streams {
			stream.cancel(errServiceClosed)
		}
		for _, lease := range readLeases {
			lease.cancel()
		}
		s.wg.Wait()
		for _, done := range streamDones {
			<-done
		}
		for _, done := range expiryDones {
			<-done
		}
		for _, lease := range readLeases {
			<-lease.done
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

func (s *Service) generateCapability(shareID string) (generatedCapability, error) {
	var secret [capabilitySecretBytes]byte
	defer clearSecret(secret[:])
	if _, err := rand.Read(secret[:]); err != nil {
		return generatedCapability{}, fmt.Errorf("generate transfer capability: %w", err)
	}
	text, hash, err := encodeCapabilityBytes(shareID, &secret)
	if err != nil {
		return generatedCapability{}, err
	}
	s.captureSecret("generated.capability", text)
	return generatedCapability{text: text, hash: hash}, nil
}

func newEntityID() string { return uuid.NewV7().String() }

// jobCapabilityAAD is intentionally versioned and binds both immutable tenant
// and job UUIDs. Neither component may be substituted during resume.
func jobCapabilityAAD(ownerID, jobID string) string {
	return "tailcat-transfer/job-capability/v1/" + ownerID + "/" + jobID
}

func shareView(row *ent.TransferShare) ShareView {
	var readyAt *time.Time
	if row.ReadyAt != nil {
		readyAt = new(row.ReadyAt.UTC())
	}
	return ShareView{
		ID: row.ID, ServerID: row.ServerID, Status: string(row.Status), TotalBytes: row.TotalBytes,
		FileCount: row.FileCount, ExpiresAt: row.ExpiresAt.UTC(), ReadyAt: readyAt,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func fileView(row *ent.ShareFile) FileView {
	return FileView{ID: row.ID, VirtualPath: row.VirtualPath, Size: row.SizeBytes, MTime: row.Mtime.UTC(), CreatedAt: row.CreatedAt.UTC()}
}

func (s *Service) recordLifecycle(ctx context.Context, ownerID, action, resourceKind, resourceID, outcome string) error {
	return s.recordLifecycleOccurrence(ctx, ownerID, action, resourceKind, resourceID, outcome, 0)
}

func (s *Service) recordLifecycleOccurrence(ctx context.Context, ownerID, action, resourceKind, resourceID, outcome string, occurrence int) error {
	entry := lifecycleAuditEntry(ownerID, action, resourceKind, resourceID, outcome, occurrence)
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

func (s *Service) recordLifecycleWithClient(ctx context.Context, client *ent.Client, ownerID, action, resourceKind, resourceID, outcome string) error {
	return s.recordLifecycleOccurrenceWithClient(ctx, client, ownerID, action, resourceKind, resourceID, outcome, 0)
}

func (s *Service) recordLifecycleOccurrenceWithClient(ctx context.Context, client *ent.Client, ownerID, action, resourceKind, resourceID, outcome string, occurrence int) error {
	entry := lifecycleAuditEntry(ownerID, action, resourceKind, resourceID, outcome, occurrence)
	if err := s.auditor.RecordWithClient(ctx, client, entry); err != nil {
		return fmt.Errorf("record transfer lifecycle transaction: %w", err)
	}
	return nil
}

func lifecycleAuditEntry(ownerID, action, resourceKind, resourceID, outcome string, occurrence int) audit.Entry {
	id := resourceKind + ":" + resourceID + ":" + action
	if occurrence > 1 {
		id += ":" + strconv.Itoa(occurrence)
	}
	return audit.Entry{
		ID: id, UserID: ownerID, Action: action,
		ResourceKind: resourceKind, ResourceID: resourceID, Outcome: outcome,
	}
}

func (s *Service) publishTransfer(ownerID, resourceID string, phase events.RuntimePhase, payload TransferEventPayload) {
	s.publisher.PublishEvent(ownerID, events.Envelope{
		Version: 1, Type: "transfer", ResourceKind: "transfer", ResourceID: resourceID,
		OperationID: resourceID, Phase: phase, Payload: payload,
	})
}

// publishJobProgress throttles nonterminal job events to at most one per
// second. Terminal lifecycle events use publishTransfer directly and are a
// distinct state transition rather than a progress sample.
func (s *Service) publishJobProgress(ownerID, jobID string, payload TransferEventPayload) {
	now := s.progressNow()
	s.mu.Lock()
	last := s.progressPublished[jobID]
	if !last.IsZero() && now.Sub(last) < time.Second {
		s.mu.Unlock()
		return
	}
	s.progressPublished[jobID] = now
	s.mu.Unlock()
	s.publishTransfer(ownerID, jobID, events.RuntimePhaseRunning, payload)
}

func (s *Service) recordFailure(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.failures = append(s.failures, err)
	s.mu.Unlock()
}

func (s *Service) commitLifecycle(tx *ent.Tx, operation string) error {
	if s.lifecycleHooks.beforeCommit != nil {
		if err := s.lifecycleHooks.beforeCommit(operation); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.lifecycleHooks.afterCommit != nil {
		s.lifecycleHooks.afterCommit(operation)
	}
	return nil
}

func (s *Service) captureSecret(kind string, secret []byte) {
	if s.secretHooks.capture != nil {
		s.secretHooks.capture(kind, secret)
	}
}
