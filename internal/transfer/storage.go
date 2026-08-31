package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/zeebo/blake3"
)

const (
	MaxFileBytes     int64 = 512 * 1024 * 1024
	MaxShareBytes    int64 = 1024 * 1024 * 1024
	MaxOwnerBytes    int64 = 2 * 1024 * 1024 * 1024
	MaxFilesPerShare       = 1000

	maxBoundaryBytes         = 1024
	maxBoundaryDepth         = 32
	randomNameBytes          = 16
	maxNameAttempts          = 128
	tempNamePrefix           = "tmp-"
	maxConsecutiveEmptyReads = 100
)

var (
	ErrInvalidPath     = errors.New("invalid transfer storage path")
	ErrSymlink         = errors.New("transfer storage symlink is forbidden")
	ErrNotRegular      = errors.New("transfer storage entry is not a regular file")
	ErrFileTooLarge    = errors.New("transfer file exceeds the size limit")
	ErrQuotaExceeded   = errors.New("transfer storage quota exceeded")
	ErrSizeMismatch    = errors.New("transfer input size does not match reservation")
	ErrReservationUsed = errors.New("transfer quota reservation is no longer pending")
	ErrNameCollision   = errors.New("could not allocate an opaque storage name")
	ErrRootChanged     = errors.New("transfer storage root changed while opening")
	ErrPermissions     = errors.New("transfer storage entry has unsafe permissions")
	ErrMultipleLinks   = errors.New("transfer storage entry has multiple hard links")
	ErrClosed          = errors.New("transfer storage is closed")
)

// StoredFile is the filesystem metadata returned after an atomic publication.
// It intentionally contains no host filesystem path.
type StoredFile struct {
	StorageName string
	Size        int64
	MTime       time.Time
	BLAKE3      string
}

// ReadHandle is an independently owned, read-only staged-file handle. It
// remains readable after Storage.Close and must be closed by its caller. Its
// narrow API intentionally exposes no mutation or publication methods.
type ReadHandle struct {
	file *os.File
}

func (h *ReadHandle) Read(buffer []byte) (int, error) {
	return h.file.Read(buffer)
}

func (h *ReadHandle) ReadAt(buffer []byte, offset int64) (int, error) {
	return h.file.ReadAt(buffer, offset)
}

func (h *ReadHandle) Seek(offset int64, whence int) (int64, error) {
	return h.file.Seek(offset, whence)
}

func (h *ReadHandle) Stat() (os.FileInfo, error) {
	return h.file.Stat()
}

func (h *ReadHandle) Close() error {
	return h.file.Close()
}

// QuotaUsage is a point-in-time view of owner and share quota consumption.
// Reserved and committed files are both included until released or removed.
type QuotaUsage struct {
	OwnerBytes int64
	ShareBytes int64
	ShareFiles int
}

type storageHooks struct {
	syncFile                      func(*os.File) error
	syncDir                       func(*os.File) error
	link                          func(*os.Root, string, string) error
	remove                        func(*os.Root, string) error
	closeVerifiedFinal            func(*os.File) error
	beforePublish                 func()
	afterIngest                   func(int64, string)
	afterPairVerified             func()
	afterLifecycleCheckBeforeLink func()
}

type constructorHooks struct {
	afterInitialLstat  func()
	syncDir            func(*os.File) error
	closeVerifiedFinal func(*os.File) error
}

// Storage owns all filesystem path construction for staged transfer bytes.
// Callers provide only canonical entity IDs and Storage-generated basenames.
type Storage struct {
	root *os.Root

	quota       *quotaLedger
	sharedQuota *sharedQuotaLedger

	random        io.Reader
	nameMu        sync.Mutex
	hooks         storageHooks
	manifestHooks manifestHooks
	reservationMu sync.Mutex
	reservations  map[*Reservation]struct{}

	lifecycleCtx    context.Context
	cancelLifecycle context.CancelCauseFunc
	lifecycleMu     sync.Mutex
	active          sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
	closed          atomic.Bool
}

type quotaKey struct {
	ownerID     string
	shareID     string
	storageName string
}

type ownerUsage struct {
	bytes  int64
	shares map[string]*shareUsage
}

type shareUsage struct {
	bytes int64
	files int
}

type quotaLedger struct {
	mu        sync.Mutex
	owners    map[string]*ownerUsage
	committed map[quotaKey]int64
}

type sharedQuotaLedger struct {
	identity os.FileInfo
	quota    quotaLedger
	refs     int
	ready    chan struct{}
	initErr  error
}

var sharedQuotaRegistry struct {
	sync.Mutex
	ledgers []*sharedQuotaLedger
}

const (
	reservationPending uint32 = iota
	reservationCommitted
	reservationReleased
)

// Reservation atomically holds one file slot and its bytes against all quota
// levels. Release is safe to call repeatedly.
type Reservation struct {
	storage       *Storage
	ownerID       string
	shareID       string
	size          int64
	state         atomic.Uint32
	guard         sync.Mutex
	committedName string
}

// NewStorage creates or opens a required, real staging root.
func NewStorage(rootPath string) (*Storage, error) {
	return newStorage(rootPath, constructorHooks{})
}

func newStorage(rootPath string, constructorHooks constructorHooks) (*Storage, error) {
	if rootPath == "" || strings.ContainsRune(rootPath, 0) {
		return nil, fmt.Errorf("%w: staging root is required", ErrInvalidPath)
	}
	info, err := os.Lstat(rootPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(rootPath, 0o700); err != nil {
			return nil, fmt.Errorf("create staging root: %w", err)
		}
		info, err = os.Lstat(rootPath)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect staging root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: staging root", ErrSymlink)
	}
	if err := validatePlatformFileInfo(info); err != nil {
		return nil, fmt.Errorf("inspect staging root platform attributes: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: staging root is not a directory", ErrInvalidPath)
	}
	if err := validatePrivateDirectory(info); err != nil {
		return nil, fmt.Errorf("staging root: %w", err)
	}
	if constructorHooks.afterInitialLstat != nil {
		constructorHooks.afterInitialLstat()
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open staging root: %w", err)
	}
	rootIdentity, err := validateRootAnchor(rootPath, root, info)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	sharedQuota, initializeQuota := acquireSharedQuota(rootIdentity)
	lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
	storage := &Storage{
		root:            root,
		random:          rand.Reader,
		quota:           &sharedQuota.quota,
		sharedQuota:     sharedQuota,
		lifecycleCtx:    lifecycleCtx,
		cancelLifecycle: cancelLifecycle,
		reservations:    make(map[*Reservation]struct{}),
	}
	storage.hooks.syncFile = (*os.File).Sync
	storage.hooks.syncDir = syncDirectory
	storage.hooks.link = (*os.Root).Link
	storage.hooks.remove = (*os.Root).Remove
	storage.hooks.closeVerifiedFinal = (*os.File).Close
	if constructorHooks.syncDir != nil {
		storage.hooks.syncDir = constructorHooks.syncDir
	}
	if constructorHooks.closeVerifiedFinal != nil {
		storage.hooks.closeVerifiedFinal = constructorHooks.closeVerifiedFinal
	}
	if initializeQuota {
		finishSharedQuotaInitialization(sharedQuota, storage.rebuildQuota())
	}
	if err := waitSharedQuotaInitialization(sharedQuota); err != nil {
		_ = root.Close()
		releaseSharedQuota(sharedQuota)
		return nil, err
	}
	return storage, nil
}

func validateRootAnchor(rootPath string, root *os.Root, initial os.FileInfo) (os.FileInfo, error) {
	anchor, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open staging root anchor: %w", err)
	}
	anchorInfo, statErr := anchor.Stat()
	platformErr := validateOpenedDirectory(anchor)
	closeErr := anchor.Close()
	current, currentErr := os.Lstat(rootPath)
	if statErr != nil || platformErr != nil || closeErr != nil || currentErr != nil {
		return nil, errors.Join(statErr, platformErr, closeErr, currentErr)
	}
	if current.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: configured root became a symlink", ErrRootChanged)
	}
	if err := validatePlatformFileInfo(current); err != nil {
		return nil, fmt.Errorf("reinspect staging root platform attributes: %w", err)
	}
	if !anchorInfo.IsDir() || !current.IsDir() || !os.SameFile(initial, anchorInfo) || !os.SameFile(anchorInfo, current) {
		return nil, ErrRootChanged
	}
	if err := validatePrivateDirectory(anchorInfo); err != nil {
		return nil, fmt.Errorf("opened staging root: %w", err)
	}
	return anchorInfo, nil
}

func acquireSharedQuota(identity os.FileInfo) (*sharedQuotaLedger, bool) {
	sharedQuotaRegistry.Lock()
	defer sharedQuotaRegistry.Unlock()
	for _, shared := range sharedQuotaRegistry.ledgers {
		if os.SameFile(identity, shared.identity) {
			shared.refs++
			return shared, false
		}
	}
	shared := &sharedQuotaLedger{
		identity: identity,
		quota: quotaLedger{
			owners:    make(map[string]*ownerUsage),
			committed: make(map[quotaKey]int64),
		},
		refs:  1,
		ready: make(chan struct{}),
	}
	sharedQuotaRegistry.ledgers = append(sharedQuotaRegistry.ledgers, shared)
	return shared, true
}

func finishSharedQuotaInitialization(shared *sharedQuotaLedger, err error) {
	sharedQuotaRegistry.Lock()
	shared.initErr = err
	close(shared.ready)
	sharedQuotaRegistry.Unlock()
}

func waitSharedQuotaInitialization(shared *sharedQuotaLedger) error {
	<-shared.ready
	sharedQuotaRegistry.Lock()
	defer sharedQuotaRegistry.Unlock()
	return shared.initErr
}

func releaseSharedQuota(shared *sharedQuotaLedger) {
	if shared == nil {
		return
	}
	sharedQuotaRegistry.Lock()
	defer sharedQuotaRegistry.Unlock()
	shared.refs--
	if shared.refs != 0 {
		return
	}
	for index, candidate := range sharedQuotaRegistry.ledgers {
		if candidate == shared {
			sharedQuotaRegistry.ledgers = append(sharedQuotaRegistry.ledgers[:index], sharedQuotaRegistry.ledgers[index+1:]...)
			return
		}
	}
}

func validatePrivateDirectory(info os.FileInfo) error {
	// Windows FileMode permission bits do not describe the directory ACL;
	// validateOpenedDirectory performs the conservative handle/DACL check there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return ErrPermissions
	}
	return nil
}

// Close cancels and joins admitted operations, then releases the rooted
// directory handle. It is idempotent.
//
// Close must not be called synchronously from an upload source's Read or Close
// method, or from a Storage test/instrumentation hook invoked inside an admitted
// operation. Close waits for that operation, so a callback waiting on Close
// would wait on itself. Signal an external owner and return from the callback
// before that owner calls Close.
func (s *Storage) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if !s.closed.Load() {
		s.cancelLifecycle(ErrClosed)
		s.closed.Store(true)
	}
	s.lifecycleMu.Unlock()
	// The exported reentrancy contract above is required: an admitted callback
	// cannot synchronously join the operation currently executing that callback.
	s.active.Wait()
	s.releasePendingReservations()
	s.closeOnce.Do(func() {
		s.closeErr = s.root.Close()
		releaseSharedQuota(s.sharedQuota)
	})
	return s.closeErr
}

func (s *Storage) beginOperation(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("%w: nil context", ErrInvalidPath)
	}
	s.lifecycleMu.Lock()
	if s.closed.Load() {
		s.lifecycleMu.Unlock()
		return nil, nil, ErrClosed
	}
	s.active.Add(1)
	s.lifecycleMu.Unlock()

	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifecycleCancel := context.AfterFunc(s.lifecycleCtx, func() {
		cancel(ErrClosed)
	})
	var endOnce sync.Once
	end := func() {
		endOnce.Do(func() {
			stopLifecycleCancel()
			cancel(nil)
			s.active.Done()
		})
	}
	return operationCtx, end, nil
}

func contextError(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func (s *Storage) operationError(ctx context.Context) error {
	if cause := context.Cause(s.lifecycleCtx); cause != nil {
		return cause
	}
	return contextError(ctx)
}

func (s *Storage) ensureOpen() error {
	if s == nil || s.root == nil || s.closed.Load() {
		return ErrClosed
	}
	return nil
}

func (s *Storage) trackReservation(reservation *Reservation) {
	s.reservationMu.Lock()
	s.reservations[reservation] = struct{}{}
	s.reservationMu.Unlock()
}

func (s *Storage) untrackReservation(reservation *Reservation) {
	s.reservationMu.Lock()
	delete(s.reservations, reservation)
	s.reservationMu.Unlock()
}

func (s *Storage) releasePendingReservations() {
	s.reservationMu.Lock()
	reservations := make([]*Reservation, 0, len(s.reservations))
	for reservation := range s.reservations {
		reservations = append(reservations, reservation)
	}
	s.reservationMu.Unlock()
	for _, reservation := range reservations {
		reservation.Release()
	}
}

// Reserve atomically admits a prospective file against file, share, owner,
// and per-share file-count limits.
func (s *Storage) Reserve(ctx context.Context, ownerID, shareID string, size int64) (*Reservation, error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.reserve(operationCtx, ownerID, shareID, size)
}

func (s *Storage) reserve(ctx context.Context, ownerID, shareID string, size int64) (*Reservation, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, fmt.Errorf("%w: negative file size", ErrInvalidPath)
	}
	if size > MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	shareRoot, err := s.openShare(ownerID, shareID, true)
	if err != nil {
		return nil, err
	}
	if err := shareRoot.Close(); err != nil {
		return nil, fmt.Errorf("close share root: %w", err)
	}

	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	owner := s.quota.owners[ownerID]
	if owner == nil {
		owner = &ownerUsage{shares: make(map[string]*shareUsage)}
		s.quota.owners[ownerID] = owner
	}
	share := owner.shares[shareID]
	if share == nil {
		share = &shareUsage{}
		owner.shares[shareID] = share
	}
	if size > MaxOwnerBytes-owner.bytes || size > MaxShareBytes-share.bytes || share.files >= MaxFilesPerShare {
		return nil, ErrQuotaExceeded
	}
	owner.bytes += size
	share.bytes += size
	share.files++
	reservation := &Reservation{storage: s, ownerID: ownerID, shareID: shareID, size: size}
	s.trackReservation(reservation)
	return reservation, nil
}

// Release returns a pending reservation to all quota levels. Committed
// reservations stay charged until Storage.Remove deletes the published file.
func (r *Reservation) Release() {
	if r == nil || r.storage == nil {
		return
	}
	r.guard.Lock()
	defer r.guard.Unlock()
	if !r.state.CompareAndSwap(reservationPending, reservationReleased) {
		return
	}
	r.storage.quota.release(r.ownerID, r.shareID, r.size)
	r.storage.untrackReservation(r)
}

// Usage returns current admitted bytes and file count.
func (s *Storage) Usage(ctx context.Context, ownerID, shareID string) (QuotaUsage, error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return QuotaUsage{}, err
	}
	defer end()
	if err := contextError(operationCtx); err != nil {
		return QuotaUsage{}, err
	}
	if err := validateEntityID(ownerID); err != nil {
		return QuotaUsage{}, fmt.Errorf("owner ID: %w", err)
	}
	if err := validateEntityID(shareID); err != nil {
		return QuotaUsage{}, fmt.Errorf("share ID: %w", err)
	}
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	owner := s.quota.owners[ownerID]
	if owner == nil {
		return QuotaUsage{}, nil
	}
	usage := QuotaUsage{OwnerBytes: owner.bytes}
	if share := owner.shares[shareID]; share != nil {
		usage.ShareBytes = share.bytes
		usage.ShareFiles = share.files
	}
	return usage, nil
}

// Store reserves quota and publishes exactly size bytes from src. Storage owns
// src and closes it on every return; Close must unblock Read, as net/http
// request bodies and io.PipeReader do.
func (s *Storage) Store(ctx context.Context, ownerID, shareID string, size int64, src io.ReadCloser) (StoredFile, error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		if src != nil {
			_ = src.Close()
		}
		return StoredFile{}, err
	}
	defer end()
	reservation, err := s.reserve(operationCtx, ownerID, shareID, size)
	if err != nil {
		if src != nil {
			_ = src.Close()
		}
		return StoredFile{}, err
	}
	return s.storeReserved(operationCtx, reservation, src)
}

// StoreReserved streams a pending reservation into a private temporary file,
// durably publishes it under a random basename, and commits the reservation.
// If a post-publication failure cannot roll the final link back, it returns a
// nonzero StoredFile with the error and keeps quota charged; the caller must
// pass that opaque StorageName to Remove for cleanup.
func (s *Storage) StoreReserved(ctx context.Context, reservation *Reservation, src io.ReadCloser) (stored StoredFile, retErr error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		if src != nil {
			_ = src.Close()
		}
		if reservation != nil && reservation.storage == s {
			reservation.Release()
		}
		return StoredFile{}, err
	}
	defer end()
	return s.storeReserved(operationCtx, reservation, src)
}

func (s *Storage) storeReserved(ctx context.Context, reservation *Reservation, src io.ReadCloser) (stored StoredFile, retErr error) {
	if src == nil {
		if reservation != nil {
			reservation.Release()
		}
		return StoredFile{}, fmt.Errorf("%w: nil reader", ErrInvalidPath)
	}
	var closeOnce sync.Once
	var sourceCloseErr error
	closeSource := func() {
		closeOnce.Do(func() {
			sourceCloseErr = src.Close()
		})
	}
	stopContextClose := context.AfterFunc(ctx, closeSource)
	defer func() {
		stopContextClose()
		closeSource()
		if sourceCloseErr != nil && !errors.Is(retErr, sourceCloseErr) {
			retErr = errors.Join(retErr, fmt.Errorf("close upload source: %w", sourceCloseErr))
		}
	}()
	if reservation == nil || reservation.storage != s {
		return StoredFile{}, fmt.Errorf("%w: wrong storage", ErrReservationUsed)
	}
	if reservation.state.Load() != reservationPending {
		return StoredFile{}, ErrReservationUsed
	}
	if err := contextError(ctx); err != nil {
		reservation.Release()
		return StoredFile{}, err
	}

	shareRoot, err := s.openShare(reservation.ownerID, reservation.shareID, true)
	if err != nil {
		reservation.Release()
		return StoredFile{}, err
	}
	defer shareRoot.Close()

	tempName, file, err := s.createTemp(shareRoot)
	if err != nil {
		reservation.Release()
		return StoredFile{}, err
	}
	tempExists := true
	fileOpen := true
	publishedName := ""
	var writtenInfo os.FileInfo
	defer func() {
		if fileOpen {
			if err := file.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close temporary file during cleanup: %w", err))
			}
		}
		if tempExists {
			if err := s.hooks.remove(shareRoot, tempName); err != nil && !errors.Is(err, fs.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("remove temporary file: %w", err))
			}
		}
		if retErr != nil {
			if publishedName == "" {
				reservation.Release()
			} else {
				s.reconcileFailedPublish(shareRoot, publishedName, writtenInfo, reservation, &retErr)
			}
		}
	}()

	hasher := blake3.New()
	written, ingestErr := copyExactAndProbe(ctx, io.MultiWriter(file, hasher), src, reservation.size)
	if s.hooks.afterIngest != nil {
		info, statErr := file.Stat()
		if statErr != nil {
			return StoredFile{}, fmt.Errorf("stat ingested temporary file: %w", statErr)
		}
		s.hooks.afterIngest(info.Size(), hex.EncodeToString(hasher.Sum(nil)))
	}
	stopContextClose()
	closeSource()
	if err := contextError(ctx); err != nil {
		return StoredFile{}, err
	}
	if ingestErr != nil {
		return StoredFile{}, fmt.Errorf("stream staged file after %d bytes: %w", written, ingestErr)
	}
	if sourceCloseErr != nil {
		return StoredFile{}, fmt.Errorf("close upload source: %w", sourceCloseErr)
	}
	if err := s.hooks.syncFile(file); err != nil {
		return StoredFile{}, fmt.Errorf("sync staged file: %w", err)
	}
	writtenInfo, err = file.Stat()
	if err != nil {
		return StoredFile{}, fmt.Errorf("stat staged file: %w", err)
	}
	if err := file.Close(); err != nil {
		fileOpen = false
		return StoredFile{}, fmt.Errorf("close staged file: %w", err)
	}
	fileOpen = false

	finalName, err := s.publishNoReplace(ctx, shareRoot, tempName)
	if err != nil {
		return StoredFile{}, fmt.Errorf("publish staged file: %w", err)
	}
	publishedName = finalName
	stored = StoredFile{
		StorageName: finalName,
		Size:        writtenInfo.Size(),
		MTime:       writtenInfo.ModTime().UTC(),
		BLAKE3:      hex.EncodeToString(hasher.Sum(nil)),
	}
	if err := s.hooks.remove(shareRoot, tempName); err != nil {
		return stored, fmt.Errorf("remove linked temporary file: %w", err)
	}
	tempExists = false
	if err := s.syncShareDirectory(shareRoot); err != nil {
		return stored, fmt.Errorf("sync published directory: %w", err)
	}

	info, err := safeRegularInfo(shareRoot, finalName)
	if err != nil {
		return stored, err
	}
	if info.Size() != reservation.size || !os.SameFile(writtenInfo, info) {
		return stored, ErrFileChanged
	}
	if err := reservation.commit(finalName); err != nil {
		return stored, err
	}
	publishedName = ""
	stored.Size = info.Size()
	stored.MTime = info.ModTime().UTC()
	return stored, nil
}

// Open opens one Storage-generated regular file without exposing its host path.
func (s *Storage) Open(ctx context.Context, ownerID, shareID, storageName string) (*ReadHandle, error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	file, err := s.open(operationCtx, ownerID, shareID, storageName)
	if err != nil {
		return nil, err
	}
	return &ReadHandle{file: file}, nil
}

func (s *Storage) open(ctx context.Context, ownerID, shareID, storageName string) (*os.File, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := validateStorageName(storageName); err != nil {
		return nil, err
	}
	shareRoot, err := s.openShare(ownerID, shareID, false)
	if err != nil {
		return nil, err
	}
	defer shareRoot.Close()
	before, err := safeRegularInfo(shareRoot, storageName)
	if err != nil {
		return nil, err
	}
	file, err := shareRoot.Open(storageName)
	if err != nil {
		return nil, fmt.Errorf("open staged file: %w", err)
	}
	if err := validateOpenedRegularFile(file, 1); err != nil {
		_ = file.Close()
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat opened staged file: %w", err)
	}
	current, err := safeRegularInfo(shareRoot, storageName)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(after, current) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: staged file changed while opening", ErrInvalidPath)
	}
	return file, nil
}

// Remove deletes one regular staged file and releases its committed quota.
func (s *Storage) Remove(ctx context.Context, ownerID, shareID, storageName string) error {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer end()
	if err := contextError(operationCtx); err != nil {
		return err
	}
	if err := validateStorageName(storageName); err != nil {
		return err
	}
	shareRoot, err := s.openShare(ownerID, shareID, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.quota.remove(ownerID, shareID, storageName)
			return nil
		}
		return err
	}
	defer shareRoot.Close()
	if _, err := s.prepareFinalEntry(shareRoot, storageName); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.quota.remove(ownerID, shareID, storageName)
			return nil
		}
		return err
	}
	if err := s.hooks.remove(shareRoot, storageName); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.quota.remove(ownerID, shareID, storageName)
			return nil
		}
		return fmt.Errorf("remove staged file: %w", err)
	}
	s.quota.remove(ownerID, shareID, storageName)
	if err := s.syncShareDirectory(shareRoot); err != nil {
		return fmt.Errorf("sync removed file directory: %w", err)
	}
	return nil
}

// CleanupTemps removes only internal temporary files in one validated share.
func (s *Storage) CleanupTemps(ctx context.Context, ownerID, shareID string) (int, error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return 0, err
	}
	defer end()
	if err := contextError(operationCtx); err != nil {
		return 0, err
	}
	shareRoot, err := s.openShare(ownerID, shareID, false)
	if err != nil {
		return 0, err
	}
	defer shareRoot.Close()
	directory, err := shareRoot.Open(".")
	if err != nil {
		return 0, fmt.Errorf("open share directory: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return 0, fmt.Errorf("read share directory: %w", err)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close share directory: %w", closeErr)
	}
	removed := 0
	for _, entry := range entries {
		if err := contextError(operationCtx); err != nil {
			return removed, err
		}
		if !isTempName(entry.Name()) {
			continue
		}
		info, err := shareRoot.Lstat(entry.Name())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("inspect temporary file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return removed, fmt.Errorf("%w: temporary file", ErrSymlink)
		}
		if !info.Mode().IsRegular() {
			return removed, fmt.Errorf("%w: temporary file", ErrNotRegular)
		}
		links, available, linkErr := rootedFileLinkCount(shareRoot, entry.Name(), info)
		if linkErr != nil {
			return removed, linkErr
		}
		if available && links == 2 {
			recovered, recoverErr := s.recoverTempAlias(shareRoot, entry.Name())
			if recovered {
				removed++
			}
			if recoverErr != nil {
				var syncErr error
				if recovered {
					if err := s.syncShareDirectory(shareRoot); err != nil {
						syncErr = fmt.Errorf("sync cleaned directory: %w", err)
					}
				}
				return removed, errors.Join(recoverErr, syncErr)
			}
			continue
		}
		if err := validateStoredFileInfo(info); err != nil {
			return removed, err
		}
		if err := shareRoot.Remove(entry.Name()); err != nil {
			return removed, fmt.Errorf("remove temporary file: %w", err)
		}
		removed++
	}
	if removed > 0 {
		if err := s.syncShareDirectory(shareRoot); err != nil {
			return removed, fmt.Errorf("sync cleaned directory: %w", err)
		}
	}
	return removed, nil
}

func (s *Storage) openShare(ownerID, shareID string, create bool) (*os.Root, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if err := validateEntityID(ownerID); err != nil {
		return nil, fmt.Errorf("owner ID: %w", err)
	}
	if err := validateEntityID(shareID); err != nil {
		return nil, fmt.Errorf("share ID: %w", err)
	}
	ownerRoot, err := openChildDirectory(s.root, ownerID, create)
	if err != nil {
		return nil, fmt.Errorf("open owner directory: %w", err)
	}
	shareRoot, err := openChildDirectory(ownerRoot, shareID, create)
	closeErr := ownerRoot.Close()
	if err != nil {
		return nil, fmt.Errorf("open share directory: %w", err)
	}
	if closeErr != nil {
		_ = shareRoot.Close()
		return nil, fmt.Errorf("close owner directory: %w", closeErr)
	}
	return shareRoot, nil
}

func openChildDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if err := validatePlatformFileInfo(info); err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalidPath
	}
	if err := validatePrivateDirectory(info); err != nil {
		return nil, err
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	anchor, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	anchorInfo, statErr := anchor.Stat()
	platformErr := validateOpenedDirectory(anchor)
	closeErr := anchor.Close()
	current, currentErr := parent.Lstat(name)
	if statErr != nil || platformErr != nil || closeErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(info, anchorInfo) || !os.SameFile(anchorInfo, current) {
		_ = child.Close()
		if statErr != nil {
			return nil, statErr
		}
		if platformErr != nil {
			return nil, platformErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if currentErr != nil {
			return nil, currentErr
		}
		if current.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSymlink
		}
		return nil, ErrInvalidPath
	}
	if err := validatePlatformFileInfo(current); err != nil {
		_ = child.Close()
		return nil, err
	}
	if err := validatePrivateDirectory(anchorInfo); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func (s *Storage) createTemp(root *os.Root) (string, *os.File, error) {
	for range maxNameAttempts {
		name, err := s.randomName(tempNamePrefix)
		if err != nil {
			return "", nil, err
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			info, statErr := file.Stat()
			var validationErr error
			if statErr == nil {
				validationErr = errors.Join(validateStoredFileInfo(info), validateOpenedRegularFile(file, 1))
			}
			if statErr != nil || validationErr != nil {
				_ = file.Close()
				_ = root.Remove(name)
				return "", nil, errors.Join(statErr, validationErr)
			}
			return name, file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("create temporary file: %w", err)
		}
	}
	return "", nil, ErrNameCollision
}

func (s *Storage) publishNoReplace(ctx context.Context, root *os.Root, tempName string) (string, error) {
	for range maxNameAttempts {
		if err := s.operationError(ctx); err != nil {
			return "", err
		}
		name, err := s.randomName("")
		if err != nil {
			return "", err
		}
		if s.hooks.beforePublish != nil {
			s.hooks.beforePublish()
		}
		if err := s.operationError(ctx); err != nil {
			return "", err
		}
		s.lifecycleMu.Lock()
		if s.closed.Load() {
			s.lifecycleMu.Unlock()
			return "", ErrClosed
		}
		if err := contextError(ctx); err != nil {
			s.lifecycleMu.Unlock()
			return "", err
		}
		if s.hooks.afterLifecycleCheckBeforeLink != nil {
			s.hooks.afterLifecycleCheckBeforeLink()
		}
		if err := contextError(ctx); err != nil {
			s.lifecycleMu.Unlock()
			return "", err
		}
		err = s.hooks.link(root, tempName, name)
		s.lifecycleMu.Unlock()
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		info, statErr := root.Lstat(name)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect storage-name collision: %w", statErr)
		}
		if statErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: storage-name collision", ErrSymlink)
		}
	}
	return "", ErrNameCollision
}

func (s *Storage) reconcileFailedPublish(root *os.Root, storageName string, writtenInfo os.FileInfo, reservation *Reservation, retErr *error) {
	removeErr := s.hooks.remove(root, storageName)
	if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
		reservation.Release()
		if removeErr == nil {
			*retErr = errors.Join(*retErr, s.syncShareDirectory(root))
		}
		return
	}
	current, statErr := root.Lstat(storageName)
	if errors.Is(statErr, fs.ErrNotExist) {
		reservation.Release()
		return
	}
	if statErr == nil && (current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || writtenInfo == nil || !os.SameFile(writtenInfo, current)) {
		reservation.Release()
		*retErr = errors.Join(*retErr, removeErr, ErrFileChanged)
		return
	}
	commitErr := reservation.commit(storageName)
	*retErr = errors.Join(*retErr, fmt.Errorf("rollback published file: %w", removeErr), statErr, commitErr)
}

func (s *Storage) randomName(prefix string) (string, error) {
	var random [randomNameBytes]byte
	s.nameMu.Lock()
	_, err := io.ReadFull(s.random, random[:])
	s.nameMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("generate storage name: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func (s *Storage) syncShareDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := s.hooks.syncDir(directory)
	closeErr := directory.Close()
	if isUnsupportedDirectorySync(syncErr) {
		syncErr = nil
	}
	return errors.Join(syncErr, closeErr)
}

func isUnsupportedDirectorySync(err error) bool {
	return err != nil && (errors.Is(err, errors.ErrUnsupported) || errors.Is(err, fs.ErrInvalid))
}

func (s *Storage) rebuildQuota() error {
	owners, err := readRootDirectory(s.root)
	if err != nil {
		return fmt.Errorf("scan staging root: %w", err)
	}
	for _, ownerEntry := range owners {
		ownerID := ownerEntry.Name()
		if err := validateEntityID(ownerID); err != nil {
			return fmt.Errorf("scan owner directory: %w", err)
		}
		ownerRoot, err := openChildDirectory(s.root, ownerID, false)
		if err != nil {
			return fmt.Errorf("scan owner directory: %w", err)
		}
		shares, err := readRootDirectory(ownerRoot)
		if err != nil {
			_ = ownerRoot.Close()
			return fmt.Errorf("scan owner shares: %w", err)
		}
		for _, shareEntry := range shares {
			shareID := shareEntry.Name()
			if err := validateEntityID(shareID); err != nil {
				_ = ownerRoot.Close()
				return fmt.Errorf("scan share directory: %w", err)
			}
			shareRoot, err := openChildDirectory(ownerRoot, shareID, false)
			if err != nil {
				_ = ownerRoot.Close()
				return fmt.Errorf("scan share directory: %w", err)
			}
			files, err := readRootDirectory(shareRoot)
			if err != nil {
				_ = shareRoot.Close()
				_ = ownerRoot.Close()
				return fmt.Errorf("scan staged files: %w", err)
			}
			recoveredAlias := false
			for _, fileEntry := range files {
				if !isTempName(fileEntry.Name()) {
					continue
				}
				info, err := privateRegularInfo(shareRoot, fileEntry.Name())
				if err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return err
				}
				links, available, linkErr := rootedFileLinkCount(shareRoot, fileEntry.Name(), info)
				if linkErr != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return linkErr
				}
				if available && links == 2 {
					recovered, recoverErr := s.recoverTempAlias(shareRoot, fileEntry.Name())
					if recovered {
						recoveredAlias = true
					}
					if recoverErr != nil {
						var syncErr error
						if recovered {
							if err := s.syncShareDirectory(shareRoot); err != nil {
								syncErr = fmt.Errorf("sync recovered share directory: %w", err)
							}
						}
						_ = shareRoot.Close()
						_ = ownerRoot.Close()
						return errors.Join(recoverErr, syncErr)
					}
				}
			}
			if recoveredAlias {
				if err := s.syncShareDirectory(shareRoot); err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return fmt.Errorf("sync recovered share directory: %w", err)
				}
				files, err = readRootDirectory(shareRoot)
				if err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return fmt.Errorf("rescan recovered staged files: %w", err)
				}
			}
			for _, fileEntry := range files {
				name := fileEntry.Name()
				if isTempName(name) {
					info, err := shareRoot.Lstat(name)
					if err != nil {
						_ = shareRoot.Close()
						_ = ownerRoot.Close()
						return fmt.Errorf("scan temporary file: %w", err)
					}
					if info.Mode()&os.ModeSymlink != 0 {
						_ = shareRoot.Close()
						_ = ownerRoot.Close()
						return fmt.Errorf("%w: temporary file", ErrSymlink)
					}
					if !info.Mode().IsRegular() {
						_ = shareRoot.Close()
						_ = ownerRoot.Close()
						return fmt.Errorf("%w: temporary file", ErrNotRegular)
					}
					if err := validateStoredFileInfo(info); err != nil {
						_ = shareRoot.Close()
						_ = ownerRoot.Close()
						return err
					}
					continue
				}
				if err := validateStorageName(name); err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return fmt.Errorf("scan storage name: %w", err)
				}
				info, err := safeRegularInfo(shareRoot, name)
				if err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return err
				}
				if info.Size() < 0 || info.Size() > MaxFileBytes {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return ErrFileTooLarge
				}
				if err := s.quota.addCommitted(ownerID, shareID, name, info.Size()); err != nil {
					_ = shareRoot.Close()
					_ = ownerRoot.Close()
					return fmt.Errorf("rebuild storage quota: %w", err)
				}
			}
			if err := shareRoot.Close(); err != nil {
				_ = ownerRoot.Close()
				return fmt.Errorf("close scanned share: %w", err)
			}
		}
		if err := ownerRoot.Close(); err != nil {
			return fmt.Errorf("close scanned owner: %w", err)
		}
	}
	return nil
}

func readRootDirectory(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	return entries, errors.Join(readErr, directory.Close())
}

func syncDirectory(directory *os.File) error {
	return directory.Sync()
}

func safeRegularInfo(root *os.Root, name string) (os.FileInfo, error) {
	info, err := privateRegularInfo(root, name)
	if err != nil {
		return nil, err
	}
	if err := validateStoredFileInfo(info); err != nil {
		return nil, err
	}
	links, available, err := rootedFileLinkCount(root, name, info)
	if err != nil {
		return nil, err
	}
	if available && links != 1 {
		return nil, ErrMultipleLinks
	}
	return info, nil
}

func privateRegularInfo(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect staged file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: staged file", ErrSymlink)
	}
	if err := validatePlatformFileInfo(info); err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: staged file", ErrNotRegular)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, ErrPermissions
	}
	return info, nil
}

func (s *Storage) prepareFinalEntry(root *os.Root, finalName string) (_ os.FileInfo, retErr error) {
	info, err := privateRegularInfo(root, finalName)
	if err != nil {
		return nil, err
	}
	links, available, err := rootedFileLinkCount(root, finalName, info)
	if err != nil {
		return nil, err
	}
	if !available || links == 1 {
		return safeRegularInfo(root, finalName)
	}
	if links != 2 {
		return nil, ErrMultipleLinks
	}
	pair, err := verifiedTempFinalPair(root, finalName)
	if err != nil {
		return nil, errors.Join(ErrMultipleLinks, err)
	}
	defer s.joinVerifiedFinalClose(pair.final, &retErr)
	if pair.finalName != finalName {
		return nil, ErrMultipleLinks
	}
	if s.hooks.afterPairVerified != nil {
		s.hooks.afterPairVerified()
	}
	if err := s.hooks.remove(root, pair.tempName); err != nil {
		return nil, fmt.Errorf("remove temporary alias: %w", err)
	}
	if err := validateRecoveredFinal(root, pair); err != nil {
		return nil, err
	}
	if err := s.syncShareDirectory(root); err != nil {
		return nil, fmt.Errorf("sync temporary-alias removal: %w", err)
	}
	return safeRegularInfo(root, finalName)
}

// recoverTempAlias reports a completed unlink and validation independently of
// an error closing the pinned final handle.
func (s *Storage) recoverTempAlias(root *os.Root, tempName string) (recovered bool, retErr error) {
	pair, err := verifiedTempFinalPair(root, tempName)
	if err != nil {
		return false, errors.Join(ErrMultipleLinks, err)
	}
	defer s.joinVerifiedFinalClose(pair.final, &retErr)
	if pair.tempName != tempName {
		return false, ErrMultipleLinks
	}
	if s.hooks.afterPairVerified != nil {
		s.hooks.afterPairVerified()
	}
	if err := s.hooks.remove(root, tempName); err != nil {
		return false, fmt.Errorf("remove temporary alias: %w", err)
	}
	if err := validateRecoveredFinal(root, pair); err != nil {
		return false, err
	}
	return true, nil
}

type verifiedPair struct {
	tempName  string
	finalName string
	final     *os.File
	finalInfo os.FileInfo
}

func (s *Storage) joinVerifiedFinalClose(final *os.File, retErr *error) {
	if err := s.hooks.closeVerifiedFinal(final); err != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("close verified final: %w", err))
	}
}

func verifiedTempFinalPair(root *os.Root, memberName string) (verifiedPair, error) {
	member, err := privateRegularInfo(root, memberName)
	if err != nil {
		return verifiedPair{}, err
	}
	links, available, err := rootedFileLinkCount(root, memberName, member)
	if err != nil {
		return verifiedPair{}, err
	}
	if !available || links != 2 {
		return verifiedPair{}, ErrMultipleLinks
	}
	entries, err := readRootDirectory(root)
	if err != nil {
		return verifiedPair{}, err
	}
	aliases := make([]string, 0, 2)
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return verifiedPair{}, err
		}
		if os.SameFile(member, info) {
			aliases = append(aliases, entry.Name())
		}
	}
	if len(aliases) != 2 {
		return verifiedPair{}, ErrMultipleLinks
	}
	var tempName, finalName string
	for _, name := range aliases {
		info, err := privateRegularInfo(root, name)
		if err != nil {
			return verifiedPair{}, err
		}
		aliasLinks, available, err := rootedFileLinkCount(root, name, info)
		if err != nil {
			return verifiedPair{}, err
		}
		if !available || aliasLinks != 2 {
			return verifiedPair{}, ErrMultipleLinks
		}
		switch {
		case isTempName(name) && tempName == "":
			tempName = name
		case !isTempName(name) && validateStorageName(name) == nil && finalName == "":
			finalName = name
		default:
			return verifiedPair{}, ErrMultipleLinks
		}
	}
	if tempName == "" || finalName == "" {
		return verifiedPair{}, ErrMultipleLinks
	}
	before, err := privateRegularInfo(root, finalName)
	if err != nil {
		return verifiedPair{}, err
	}
	final, err := root.Open(finalName)
	if err != nil {
		return verifiedPair{}, err
	}
	opened, err := final.Stat()
	if err != nil {
		_ = final.Close()
		return verifiedPair{}, err
	}
	after, err := privateRegularInfo(root, finalName)
	if err != nil {
		_ = final.Close()
		return verifiedPair{}, err
	}
	if err := validateStableFileIdentity(before, opened, after); err != nil || !os.SameFile(member, opened) {
		_ = final.Close()
		return verifiedPair{}, errors.Join(err, ErrFileChanged)
	}
	if err := validateOpenedRegularFile(final, 2); err != nil {
		_ = final.Close()
		return verifiedPair{}, err
	}
	return verifiedPair{tempName: tempName, finalName: finalName, final: final, finalInfo: opened}, nil
}

func validateRecoveredFinal(root *os.Root, pair verifiedPair) error {
	pathInfo, err := privateRegularInfo(root, pair.finalName)
	if err != nil {
		return fmt.Errorf("reinspect recovered final: %w", err)
	}
	handleInfo, err := pair.final.Stat()
	if err != nil {
		return fmt.Errorf("restat recovered final handle: %w", err)
	}
	if err := validateStableFileIdentity(pair.finalInfo, handleInfo, pathInfo); err != nil {
		return err
	}
	if err := validateOpenedRegularFile(pair.final, 1); err != nil {
		return err
	}
	if _, err := safeRegularInfo(root, pair.finalName); err != nil {
		return fmt.Errorf("validate recovered final: %w", err)
	}
	return nil
}

func validateStableFileIdentity(before, opened, after os.FileInfo) error {
	if before == nil || opened == nil || after == nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return ErrFileChanged
	}
	return nil
}

func validateStoredFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	// Windows FileMode permission bits do not describe the file ACL. Files are
	// still created with 0600 and protected by the private configured root.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return ErrPermissions
	}
	if links, ok := fileLinkCount(info); ok && links != 1 {
		return ErrMultipleLinks
	}
	return nil
}

func validateEntityID(value string) error {
	if err := validateBoundary(value); err != nil {
		return err
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed[6]>>4 != 7 || parsed[8]&0xc0 != 0x80 {
		return fmt.Errorf("%w: ID must be a canonical UUIDv7", ErrInvalidPath)
	}
	return nil
}

func validateStorageName(name string) error {
	if err := validateBoundary(name); err != nil {
		return err
	}
	if len(name) < 16 || len(name) > 128 {
		return fmt.Errorf("%w: storage name length", ErrInvalidPath)
	}
	for index, character := range []byte(name) {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && (character != '_' || index == 0) && (character != '-' || index == 0) {
			return fmt.Errorf("%w: storage name format", ErrInvalidPath)
		}
	}
	return nil
}

func validateBoundary(value string) error {
	if value == "" || len(value) > maxBoundaryBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || value == "." || value == ".." {
		return ErrInvalidPath
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsAny(value, "/\\:") {
		return ErrInvalidPath
	}
	if len(strings.Split(value, "/")) > maxBoundaryDepth {
		return ErrInvalidPath
	}
	canonical := strings.TrimRight(value, " .")
	if canonical != value || canonical == "" || isWindowsDeviceName(canonical) {
		return ErrInvalidPath
	}
	return nil
}

func isWindowsDeviceName(name string) bool {
	base := strings.ToUpper(name)
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func isTempName(name string) bool {
	if !strings.HasPrefix(name, tempNamePrefix) || len(name) != len(tempNamePrefix)+2*randomNameBytes {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, tempNamePrefix))
	return err == nil
}

func (r *Reservation) commit(storageName string) error {
	r.guard.Lock()
	defer r.guard.Unlock()
	if r.state.Load() == reservationCommitted && r.committedName == storageName {
		return nil
	}
	if !r.state.CompareAndSwap(reservationPending, reservationCommitted) {
		return ErrReservationUsed
	}
	r.committedName = storageName
	r.storage.quota.mu.Lock()
	defer r.storage.quota.mu.Unlock()
	r.storage.quota.committed[quotaKey{ownerID: r.ownerID, shareID: r.shareID, storageName: storageName}] = r.size
	r.storage.untrackReservation(r)
	return nil
}

func (q *quotaLedger) release(ownerID, shareID string, size int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked(ownerID, shareID, size)
}

func (q *quotaLedger) addCommitted(ownerID, shareID, storageName string, size int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	owner := q.owners[ownerID]
	if owner == nil {
		owner = &ownerUsage{shares: make(map[string]*shareUsage)}
		q.owners[ownerID] = owner
	}
	share := owner.shares[shareID]
	if share == nil {
		share = &shareUsage{}
		owner.shares[shareID] = share
	}
	if size > MaxOwnerBytes-owner.bytes || size > MaxShareBytes-share.bytes || share.files >= MaxFilesPerShare {
		return ErrQuotaExceeded
	}
	owner.bytes += size
	share.bytes += size
	share.files++
	q.committed[quotaKey{ownerID: ownerID, shareID: shareID, storageName: storageName}] = size
	return nil
}

func (q *quotaLedger) remove(ownerID, shareID, storageName string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := quotaKey{ownerID: ownerID, shareID: shareID, storageName: storageName}
	size, ok := q.committed[key]
	if !ok {
		return
	}
	delete(q.committed, key)
	q.releaseLocked(ownerID, shareID, size)
}

func (q *quotaLedger) releaseLocked(ownerID, shareID string, size int64) {
	owner := q.owners[ownerID]
	if owner == nil {
		return
	}
	share := owner.shares[shareID]
	if share == nil {
		return
	}
	owner.bytes -= size
	share.bytes -= size
	share.files--
	if share.files == 0 {
		delete(owner.shares, shareID)
	}
	if len(owner.shares) == 0 {
		delete(q.owners, ownerID)
	}
}

func copyExactAndProbe(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	buffer := make([]byte, 128*1024)
	remaining := size
	var written int64
	emptyReads := 0
	for remaining > 0 {
		if err := contextError(ctx); err != nil {
			return written, err
		}
		readBuffer := buffer[:min(int64(len(buffer)), remaining)]
		read, readErr := src.Read(readBuffer)
		if read < 0 || read > len(readBuffer) {
			return written, fmt.Errorf("%w: invalid reader count %d", ErrSizeMismatch, read)
		}
		if read > 0 {
			emptyReads = 0
			write, writeErr := dst.Write(readBuffer[:read])
			written += int64(write)
			remaining -= int64(write)
			if writeErr != nil {
				return written, writeErr
			}
			if write != read {
				return written, io.ErrShortWrite
			}
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= maxConsecutiveEmptyReads {
				return written, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if err := contextError(ctx); err != nil {
				return written, err
			}
			if errors.Is(readErr, io.EOF) {
				if remaining != 0 {
					return written, ErrSizeMismatch
				}
				break
			}
			return written, readErr
		}
	}

	emptyReads = 0
	var probe [1]byte
	for {
		if err := contextError(ctx); err != nil {
			return written, err
		}
		read, readErr := src.Read(probe[:])
		if read < 0 || read > len(probe) {
			return written, fmt.Errorf("%w: invalid probe count %d", ErrSizeMismatch, read)
		}
		if read > 0 {
			if size == MaxFileBytes {
				return written, ErrFileTooLarge
			}
			return written, ErrSizeMismatch
		}
		if readErr != nil {
			if err := contextError(ctx); err != nil {
				return written, err
			}
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
		emptyReads++
		if emptyReads >= maxConsecutiveEmptyReads {
			return written, io.ErrNoProgress
		}
	}
}
