package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
)

// PartialFile is a private, bounded random-name sparse writer. It exposes no
// host path or general file handle. Call Sync before persisting completed
// blocks, and Close when the current runner stops using it.
type PartialFile struct {
	storage     *Storage
	file        *os.File
	ctx         context.Context
	end         func()
	stopCancel  func() bool
	storageName string
	size        int64
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

func (file *PartialFile) StorageName() string { return file.storageName }
func (file *PartialFile) Size() int64         { return file.size }

func (file *PartialFile) WriteAt(data []byte, offset int64) (int, error) {
	if file == nil || file.file == nil || file.closed.Load() {
		return 0, ErrClosed
	}
	if offset < 0 || offset > file.size || int64(len(data)) > file.size-offset {
		return 0, fmt.Errorf("%w: partial write exceeds declared size", ErrInvalidPath)
	}
	if err := contextError(file.ctx); err != nil {
		return 0, err
	}
	written, err := file.file.WriteAt(data, offset)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if cause := contextError(file.ctx); cause != nil {
		return written, cause
	}
	return written, err
}

func (file *PartialFile) Sync() error {
	if file == nil || file.file == nil || file.closed.Load() {
		return ErrClosed
	}
	if err := contextError(file.ctx); err != nil {
		return err
	}
	if err := file.storage.hooks.syncFile(file.file); err != nil {
		return fmt.Errorf("sync partial file: %w", err)
	}
	return contextError(file.ctx)
}

func (file *PartialFile) Close() error {
	if file == nil {
		return nil
	}
	file.closeOnce.Do(func() {
		file.closed.Store(true)
		if file.stopCancel != nil {
			file.stopCancel()
		}
		if file.file != nil {
			file.closeErr = file.file.Close()
		}
		if file.end != nil {
			file.end()
		}
	})
	return file.closeErr
}

// CreatePartial reserves quota and durably creates a private sparse file with
// a cryptographically random Storage-owned basename.
func (s *Storage) CreatePartial(ctx context.Context, ownerID, jobID string, size int64) (partial *PartialFile, retErr error) {
	return s.CreatePartialScoped(ctx, ownerID, jobID, size, ScopeLimits{MaxBytes: s.limits.MaxScopeBytes, MaxFiles: s.limits.MaxFilesPerScope})
}

// CreatePartialScoped reserves one incoming item against the job-specific
// scope limit before creating or sizing any file.
func (s *Storage) CreatePartialScoped(ctx context.Context, ownerID, jobID string, size int64, scopeLimits ScopeLimits) (partial *PartialFile, retErr error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			end()
		}
	}()
	reservation, err := s.reserve(operationCtx, ownerID, jobID, size, scopeLimits)
	if err != nil {
		return nil, err
	}
	shareRoot, err := s.openShare(ownerID, jobID, false)
	if err != nil {
		reservation.Release()
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, shareRoot.Close()) }()

	name, opened, err := s.createRandomPartial(shareRoot)
	if err != nil {
		reservation.Release()
		return nil, err
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		retErr = errors.Join(retErr, opened.Close())
		removeErr := s.hooks.remove(shareRoot, name)
		if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
			reservation.Release()
			if removeErr == nil {
				retErr = errors.Join(retErr, s.syncShareDirectory(shareRoot))
			}
			return
		}
		retErr = errors.Join(retErr, fmt.Errorf("rollback partial file: %w", removeErr), reservation.commit(name))
		partial = &PartialFile{storage: s, storageName: name, size: size}
		partial.closed.Store(true)
	}()

	if err := opened.Truncate(size); err != nil {
		return nil, fmt.Errorf("size partial file: %w", err)
	}
	if err := s.hooks.syncFile(opened); err != nil {
		return nil, fmt.Errorf("sync new partial file: %w", err)
	}
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat new partial file: %w", err)
	}
	pathInfo, err := safeRegularInfo(shareRoot, name)
	if err != nil {
		return nil, err
	}
	if openedInfo.Size() != size || !os.SameFile(openedInfo, pathInfo) {
		return nil, ErrFileChanged
	}
	if err := validateOpenedRegularFile(opened, 1); err != nil {
		return nil, err
	}
	if err := s.syncShareDirectory(shareRoot); err != nil {
		return nil, fmt.Errorf("sync partial directory: %w", err)
	}
	if err := reservation.commit(name); err != nil {
		return nil, fmt.Errorf("commit partial quota: %w", err)
	}
	partial = newPartialFile(s, operationCtx, end, opened, name, size)
	transferred = true
	cleanup = false
	return partial, nil
}

// OpenPartial reopens one previously committed private sparse file by its
// owner/job/name identity and verifies its declared size before writing.
func (s *Storage) OpenPartial(ctx context.Context, ownerID, jobID, storageName string, size int64) (_ *PartialFile, retErr error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			end()
		}
	}()
	if err := validateStorageName(storageName); err != nil || size < 0 || size > MaxFileBytes {
		return nil, fmt.Errorf("%w: invalid partial identity", ErrInvalidPath)
	}
	if committed, ok := s.quota.committedSize(ownerID, jobID, storageName); !ok || committed != size {
		return nil, fmt.Errorf("%w: uncommitted partial file", ErrInvalidPath)
	}
	shareRoot, err := s.openShare(ownerID, jobID, false)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, shareRoot.Close()) }()
	before, err := safeRegularInfo(shareRoot, storageName)
	if err != nil {
		return nil, err
	}
	if before.Size() != size {
		return nil, ErrFileChanged
	}
	opened, err := shareRoot.OpenFile(storageName, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}
	defer func() {
		if !transferred {
			retErr = errors.Join(retErr, opened.Close())
		}
	}()
	if err := validateOpenedRegularFile(opened, 1); err != nil {
		return nil, err
	}
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened partial file: %w", err)
	}
	after, err := safeRegularInfo(shareRoot, storageName)
	if err != nil {
		return nil, err
	}
	if openedInfo.Size() != size || validateStableFileIdentity(before, openedInfo, after) != nil {
		return nil, ErrFileChanged
	}
	partial := newPartialFile(s, operationCtx, end, opened, storageName, size)
	transferred = true
	return partial, nil
}

func (s *Storage) createRandomPartial(root *os.Root) (string, *os.File, error) {
	for range maxNameAttempts {
		name, err := s.randomName("")
		if err != nil {
			return "", nil, err
		}
		if err := validateStorageName(name); err != nil {
			return "", nil, err
		}
		opened, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return name, opened, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return "", nil, fmt.Errorf("create partial file: %w", err)
	}
	return "", nil, ErrNameCollision
}

func newPartialFile(storage *Storage, ctx context.Context, end func(), opened *os.File, storageName string, size int64) *PartialFile {
	partial := &PartialFile{storage: storage, file: opened, ctx: ctx, end: end, storageName: storageName, size: size}
	partial.stopCancel = context.AfterFunc(ctx, func() { _ = partial.Close() })
	return partial
}

func (q *quotaLedger) committedSize(ownerID, shareID, storageName string) (int64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	size, ok := q.committed[quotaKey{ownerID: ownerID, shareID: shareID, storageName: storageName}]
	return size, ok
}
