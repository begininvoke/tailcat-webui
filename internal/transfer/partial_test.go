package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"
)

func TestPartialFileSupportsBoundedSparseWriteSyncAndRestart(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	ownerID := uuid.NewV7().String()
	jobID := uuid.NewV7().String()
	size := BlockSize + 3
	partial, err := storage.CreatePartial(t.Context(), ownerID, jobID, size)
	if err != nil {
		t.Fatalf("CreatePartial: %v", err)
	}
	name := partial.StorageName()
	if validateStorageName(name) != nil {
		t.Fatalf("partial storage name = %q", name)
	}
	if _, err := partial.WriteAt([]byte("abc"), BlockSize); err != nil {
		t.Fatalf("sparse WriteAt: %v", err)
	}
	for _, offset := range []int64{-1, size - 1, size} {
		if _, err := partial.WriteAt([]byte("too long"), offset); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("WriteAt offset %d error = %v, want ErrInvalidPath", offset, err)
		}
	}
	if err := partial.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("Close partial: %v", err)
	}
	if got := requirePartialUsage(t, storage, ownerID, jobID); got != (QuotaUsage{OwnerBytes: size, OwnerFiles: 1, ShareBytes: size, ShareFiles: 1}) {
		t.Fatalf("partial quota = %+v", got)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close Storage: %v", err)
	}

	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("restart Storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	partial, err = reopened.OpenPartial(t.Context(), ownerID, jobID, name, size)
	if err != nil {
		t.Fatalf("OpenPartial after restart: %v", err)
	}
	if _, err := partial.WriteAt([]byte{'z'}, 0); err != nil {
		t.Fatalf("repair WriteAt: %v", err)
	}
	if err := partial.Sync(); err != nil {
		t.Fatalf("repair Sync: %v", err)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("Close repaired partial: %v", err)
	}
	if got := requirePartialUsage(t, reopened, ownerID, jobID); got != (QuotaUsage{OwnerBytes: size, OwnerFiles: 1, ShareBytes: size, ShareFiles: 1}) {
		t.Fatalf("restart quota = %+v", got)
	}
	if err := reopened.Remove(t.Context(), ownerID, jobID, name); err != nil {
		t.Fatalf("Remove partial: %v", err)
	}
}

func TestPartialCreateFailureRemovesFileAndRollsBackQuota(t *testing.T) {
	storage := newTestStorage(t)
	ownerID := uuid.NewV7().String()
	jobID := uuid.NewV7().String()
	syncFailure := errors.New("partial directory sync failed")
	storage.hooks.syncDir = func(*os.File) error { return syncFailure }

	partial, err := storage.CreatePartial(t.Context(), ownerID, jobID, 17)
	if partial != nil {
		_ = partial.Close()
		t.Fatal("CreatePartial returned a handle after successful rollback")
	}
	if !errors.Is(err, syncFailure) {
		t.Fatalf("CreatePartial error = %v, want sync failure", err)
	}
	if got := requirePartialUsage(t, storage, ownerID, jobID); got != (QuotaUsage{}) {
		t.Fatalf("quota after rollback = %+v", got)
	}
	entries, readErr := os.ReadDir(filepath.Join(storage.root.Name(), ownerID, jobID))
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial rollback left entries: %v", entryNames(entries))
	}
}

func TestStorageCloseCancelsAndJoinsOpenPartialHandles(t *testing.T) {
	storage := newTestStorage(t)
	ownerID := uuid.NewV7().String()
	jobID := uuid.NewV7().String()
	partial, err := storage.CreatePartial(t.Context(), ownerID, jobID, 1)
	if err != nil {
		t.Fatalf("CreatePartial: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- storage.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Storage.Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Storage.Close did not cancel and join partial handle")
	}
	if _, err := partial.WriteAt([]byte{'x'}, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteAt after Storage.Close error = %v, want ErrClosed", err)
	}
	if err := partial.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("idempotent partial Close: %v", err)
	}
}

func TestCleanupOrphansKeepsMetadataIdentitiesAndRemovesUntrackedFinals(t *testing.T) {
	storage := newTestStorage(t)
	ownerID := uuid.NewV7().String()
	jobID := uuid.NewV7().String()
	kept, err := storage.CreatePartial(t.Context(), ownerID, jobID, 2)
	if err != nil {
		t.Fatal(err)
	}
	keptName := kept.StorageName()
	if err := kept.Close(); err != nil {
		t.Fatal(err)
	}
	orphan, err := storage.CreatePartial(t.Context(), ownerID, jobID, 3)
	if err != nil {
		t.Fatal(err)
	}
	orphanName := orphan.StorageName()
	if err := orphan.Close(); err != nil {
		t.Fatal(err)
	}
	removed, err := storage.CleanupOrphans(t.Context(), []StoredIdentity{{OwnerID: ownerID, ScopeID: jobID, StorageName: keptName}})
	if err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed orphans = %d, want 1", removed)
	}
	handle, err := storage.Open(t.Context(), ownerID, jobID, keptName)
	if err != nil {
		t.Fatalf("retained file missing: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close retained file: %v", err)
	}
	if _, err := storage.Open(t.Context(), ownerID, jobID, orphanName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan file error = %v, want not exist", err)
	}
	if got := requirePartialUsage(t, storage, ownerID, jobID); got != (QuotaUsage{OwnerBytes: 2, OwnerFiles: 1, ShareBytes: 2, ShareFiles: 1}) {
		t.Fatalf("quota after orphan cleanup = %+v", got)
	}
}

func requirePartialUsage(t *testing.T, storage *Storage, ownerID, jobID string) QuotaUsage {
	t.Helper()
	usage, err := storage.Usage(t.Context(), ownerID, jobID)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	return usage
}
