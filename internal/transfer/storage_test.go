package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testOwnerID  = "01900000-0000-7000-8000-000000000001"
	testShareID  = "01900000-0000-7000-8000-000000000002"
	otherShareID = "01900000-0000-7000-8000-000000000003"
)

func TestNewStorageRequiresSafeRootAndCreatesPrivateDirectories(t *testing.T) {
	if _, err := NewStorage(""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("NewStorage empty root error = %v, want ErrInvalidPath", err)
	}

	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()

	for _, path := range []string{
		filepath.Join(rootPath, testOwnerID),
		filepath.Join(rootPath, testOwnerID, testShareID),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("mode for %q = %o, want 700", path, got)
		}
	}
}

func TestNewStorageRejectsFileAndSymlinkRoots(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile root fixture: %v", err)
	}
	if _, err := NewStorage(filePath); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("file root error = %v, want ErrInvalidPath", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	realRoot := t.TempDir()
	linkPath := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, linkPath); err != nil {
		t.Fatalf("Symlink root fixture: %v", err)
	}
	if _, err := NewStorage(linkPath); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink root error = %v, want ErrSymlink", err)
	}
}

func TestStorageRejectsUnsafeBoundaries(t *testing.T) {
	storage := newTestStorage(t)
	validName := strings.Repeat("a", 32)
	unsafe := []string{
		"", ".", "..", "../escape", "/absolute", "sibling/../escape",
		"with\x00nul", `C:\escape`, `C:escape`, `\\server\share`, `\\?\C:\escape`,
		"CON", "con.txt", "NUL ", "COM1.log", strings.Repeat("a", 1025),
		strings.Repeat("segment/", 33) + "file",
	}

	for _, value := range unsafe {
		t.Run(fmt.Sprintf("owner_%q", value), func(t *testing.T) {
			if _, err := storage.Reserve(t.Context(), value, testShareID, 1); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Reserve owner error = %v, want ErrInvalidPath", err)
			}
		})
		t.Run(fmt.Sprintf("share_%q", value), func(t *testing.T) {
			if _, err := storage.Reserve(t.Context(), testOwnerID, value, 1); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Reserve share error = %v, want ErrInvalidPath", err)
			}
		})
		t.Run(fmt.Sprintf("read_%q", value), func(t *testing.T) {
			if _, err := storage.Open(t.Context(), testOwnerID, testShareID, value); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Open error = %v, want ErrInvalidPath", err)
			}
		})
		t.Run(fmt.Sprintf("remove_%q", value), func(t *testing.T) {
			if err := storage.Remove(t.Context(), testOwnerID, testShareID, value); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Remove error = %v, want ErrInvalidPath", err)
			}
		})
		t.Run(fmt.Sprintf("usage_owner_%q", value), func(t *testing.T) {
			if _, err := storage.Usage(t.Context(), value, testShareID); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Usage owner error = %v, want ErrInvalidPath", err)
			}
		})
		t.Run(fmt.Sprintf("usage_share_%q", value), func(t *testing.T) {
			if _, err := storage.Usage(t.Context(), testOwnerID, value); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Usage share error = %v, want ErrInvalidPath", err)
			}
		})
	}

	if _, err := storage.Open(t.Context(), "../"+testOwnerID, testShareID, validName); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("sibling-prefix Open error = %v, want ErrInvalidPath", err)
	}
}

func TestStorageRejectsSymlinkDirectoriesInsideAndOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows; representation validation is covered separately")
	}

	rootPath := t.TempDir()
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	realOwner := filepath.Join(rootPath, "real-owner")
	if err := os.Mkdir(realOwner, 0o700); err != nil {
		t.Fatalf("Mkdir real owner: %v", err)
	}
	if err := os.Symlink("real-owner", filepath.Join(rootPath, testOwnerID)); err != nil {
		t.Fatalf("Symlink same-root owner: %v", err)
	}
	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1); !errors.Is(err, ErrSymlink) {
		t.Fatalf("same-root owner symlink error = %v, want ErrSymlink", err)
	}

	if err := os.Remove(filepath.Join(rootPath, testOwnerID)); err != nil {
		t.Fatalf("Remove owner symlink: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootPath, testOwnerID)); err != nil {
		t.Fatalf("Symlink outside owner: %v", err)
	}
	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1); !errors.Is(err, ErrSymlink) {
		t.Fatalf("outside owner symlink error = %v, want ErrSymlink", err)
	}

	if err := os.Remove(filepath.Join(rootPath, testOwnerID)); err != nil {
		t.Fatalf("Remove outside owner symlink: %v", err)
	}
	ownerPath := filepath.Join(rootPath, testOwnerID)
	if err := os.Mkdir(ownerPath, 0o700); err != nil {
		t.Fatalf("Mkdir owner: %v", err)
	}
	if err := os.Mkdir(filepath.Join(ownerPath, "real-share"), 0o700); err != nil {
		t.Fatalf("Mkdir real share: %v", err)
	}
	if err := os.Symlink("real-share", filepath.Join(ownerPath, testShareID)); err != nil {
		t.Fatalf("Symlink same-root share: %v", err)
	}
	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1); !errors.Is(err, ErrSymlink) {
		t.Fatalf("same-root share symlink error = %v, want ErrSymlink", err)
	}
}

func TestStorageRejectsSymlinkAndNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows; representation validation is covered separately")
	}

	storage := newTestStorage(t)
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()
	sharePath := storageTestSharePath(storage)
	regularName := strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(sharePath, regularName), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: strings.Repeat("b", 32), target: regularName},
		{name: strings.Repeat("c", 32), target: filepath.Join(t.TempDir(), "outside")},
	} {
		if filepath.IsAbs(tc.target) {
			if err := os.WriteFile(tc.target, []byte("outside"), 0o600); err != nil {
				t.Fatalf("Write outside: %v", err)
			}
		}
		if err := os.Symlink(tc.target, filepath.Join(sharePath, tc.name)); err != nil {
			t.Fatalf("Symlink %s: %v", tc.name, err)
		}
		if _, err := storage.Open(t.Context(), testOwnerID, testShareID, tc.name); !errors.Is(err, ErrSymlink) {
			t.Errorf("Open symlink error = %v, want ErrSymlink", err)
		}
		if err := storage.Remove(t.Context(), testOwnerID, testShareID, tc.name); !errors.Is(err, ErrSymlink) {
			t.Errorf("Remove symlink error = %v, want ErrSymlink", err)
		}
	}

	directoryName := strings.Repeat("d", 32)
	if err := os.Mkdir(filepath.Join(sharePath, directoryName), 0o700); err != nil {
		t.Fatalf("Mkdir file-shaped directory: %v", err)
	}
	if _, err := storage.Open(t.Context(), testOwnerID, testShareID, directoryName); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open directory error = %v, want ErrNotRegular", err)
	}
}

func TestQuotaReservationsAreAtomicAndIdempotent(t *testing.T) {
	storage := newTestStorage(t)

	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes+1); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("over-file reservation error = %v, want ErrFileTooLarge", err)
	}
	exact, err := storage.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("exact-file reservation: %v", err)
	}
	exact.Release()
	exact.Release()
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after idempotent release = %+v, want zero", got)
	}

	const attempts = 32
	start := make(chan struct{})
	reservations := make(chan *Reservation, attempts)
	errorsCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		shareID := fmt.Sprintf("01900000-0000-7000-8000-%012d", i+10)
		wg.Go(func() {
			<-start
			reservation, err := storage.Reserve(t.Context(), testOwnerID, shareID, MaxFileBytes/2)
			if err != nil {
				errorsCh <- err
				return
			}
			reservations <- reservation
		})
	}
	close(start)
	wg.Wait()
	close(reservations)
	close(errorsCh)

	var admitted int
	for reservation := range reservations {
		admitted++
		reservation.Release()
	}
	if admitted != int(MaxOwnerBytes/(MaxFileBytes/2)) {
		t.Fatalf("concurrent reservations admitted %d, want %d", admitted, MaxOwnerBytes/(MaxFileBytes/2))
	}
	for err := range errorsCh {
		if !errors.Is(err, ErrQuotaExceeded) {
			t.Errorf("rejected reservation error = %v, want ErrQuotaExceeded", err)
		}
	}
}

func TestQuotaEnforcesShareBytesAndFileCount(t *testing.T) {
	storage := newTestStorage(t)
	first, err := storage.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("Reserve first half of share bytes: %v", err)
	}
	second, err := storage.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("Reserve second half of share bytes: %v", err)
	}
	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-share reservation error = %v, want ErrQuotaExceeded", err)
	}
	first.Release()
	second.Release()

	reservations := make([]*Reservation, 0, MaxFilesPerShare)
	for range MaxFilesPerShare {
		reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0)
		if err != nil {
			t.Fatalf("Reserve zero-byte file %d: %v", len(reservations), err)
		}
		reservations = append(reservations, reservation)
	}
	if _, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-file-count reservation error = %v, want ErrQuotaExceeded", err)
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
}

func TestReservationCommitIsIdempotentAndStaysCharged(t *testing.T) {
	storage := newTestStorage(t)
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	stored, err := storage.StoreReserved(t.Context(), reservation, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("StoreReserved: %v", err)
	}
	if err := reservation.commit(stored.StorageName); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	reservation.Release()
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 1, ShareBytes: 1, ShareFiles: 1}) {
		t.Fatalf("usage after repeated commit/release = %+v", got)
	}
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestStorePublishesAtomicallyAndRemoveReleasesCommittedQuota(t *testing.T) {
	storage := newTestStorage(t)
	entered := make(chan struct{})
	continueRename := make(chan struct{})
	storage.hooks.beforeRename = func() {
		close(entered)
		<-continueRename
	}

	type result struct {
		file StoredFile
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		file, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, strings.NewReader("abc"))
		resultCh <- result{file: file, err: err}
	}()
	<-entered

	entries, err := os.ReadDir(storageTestSharePath(storage))
	if err != nil {
		t.Fatalf("ReadDir before rename: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), tempNamePrefix) {
		t.Fatalf("entries before rename = %v, want one internal temp", entryNames(entries))
	}
	close(continueRename)
	stored := <-resultCh
	if stored.err != nil {
		t.Fatalf("Store: %v", stored.err)
	}
	if stored.file.StorageName == "" || stored.file.Size != 3 || stored.file.BLAKE3 != "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85" {
		t.Fatalf("stored metadata = %+v", stored.file)
	}
	if stored.file.MTime.Location() != time.UTC {
		t.Fatalf("mtime location = %v, want UTC", stored.file.MTime.Location())
	}

	handle, err := storage.Open(t.Context(), testOwnerID, testShareID, stored.file.StorageName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	content, err := io.ReadAll(handle)
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("Read stored file: %v", err)
	}
	if string(content) != "abc" {
		t.Fatalf("stored content = %q, want abc", content)
	}
	info, err := os.Stat(filepath.Join(storageTestSharePath(storage), stored.file.StorageName))
	if err != nil {
		t.Fatalf("Stat stored file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("stored mode = %o, want 600", got)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("committed usage = %+v", got)
	}

	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.file.StorageName); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after Remove = %+v, want zero", got)
	}
}

func TestStoreFailuresRemoveTempsAndReleaseReservation(t *testing.T) {
	tests := []struct {
		name      string
		ctx       func() context.Context
		size      int64
		reader    func() io.Reader
		configure func(*Storage)
	}{
		{name: "partial", ctx: context.Background, size: 4, reader: func() io.Reader { return strings.NewReader("abc") }},
		{name: "oversized", ctx: context.Background, size: 3, reader: func() io.Reader { return strings.NewReader("abcd") }},
		{name: "reader_error", ctx: context.Background, size: 3, reader: func() io.Reader { return io.MultiReader(strings.NewReader("a"), errorReader{}) }},
		{name: "canceled", ctx: canceledContext, size: 3, reader: func() io.Reader { return strings.NewReader("abc") }},
		{name: "file_sync", ctx: context.Background, size: 3, reader: func() io.Reader { return strings.NewReader("abc") }, configure: func(s *Storage) { s.hooks.syncFile = func(*os.File) error { return errors.New("sync failed") } }},
		{name: "rename", ctx: context.Background, size: 3, reader: func() io.Reader { return strings.NewReader("abc") }, configure: func(s *Storage) {
			s.hooks.rename = func(*os.Root, string, string) error { return errors.New("rename failed") }
		}},
		{name: "directory_sync", ctx: context.Background, size: 3, reader: func() io.Reader { return strings.NewReader("abc") }, configure: func(s *Storage) {
			s.hooks.syncDir = func(*os.File) error { return errors.New("directory sync failed") }
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storage := newTestStorage(t)
			if tc.configure != nil {
				tc.configure(storage)
			}
			if _, err := storage.Store(tc.ctx(), testOwnerID, testShareID, tc.size, tc.reader()); err == nil {
				t.Fatal("Store unexpectedly succeeded")
			}
			if got := requireUsage(t, storage); got != (QuotaUsage{}) {
				t.Fatalf("usage after failed Store = %+v, want zero", got)
			}
			entries, err := os.ReadDir(storageTestSharePath(storage))
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed Store left entries: %v", entryNames(entries))
			}
		})
	}
}

func TestStoreCancellationAfterPartialReadRemovesTempAndQuota(t *testing.T) {
	storage := newTestStorage(t)
	ctx, cancel := context.WithCancel(t.Context())
	reader := &cancelAfterFirstRead{cancel: cancel}
	if _, err := storage.Store(ctx, testOwnerID, testShareID, 3, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("Store cancellation error = %v, want context.Canceled", err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after cancellation = %+v", got)
	}
	entries, err := os.ReadDir(storageTestSharePath(storage))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled Store left entries: %v", entryNames(entries))
	}
}

func TestStoreRetriesExistingFinalNameWithoutOverwriting(t *testing.T) {
	storage := newTestStorage(t)
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()
	existingName := strings.Repeat("01", 16)
	existingPath := filepath.Join(storageTestSharePath(storage), existingName)
	if err := os.WriteFile(existingPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("Write existing final: %v", err)
	}
	storage.random = bytes.NewReader(append(append(bytes.Repeat([]byte{0}, 16), bytes.Repeat([]byte{1}, 16)...), bytes.Repeat([]byte{2}, 16)...))

	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if stored.StorageName != strings.Repeat("02", 16) {
		t.Fatalf("storage name = %q, want retry name", stored.StorageName)
	}
	content, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("Read existing final: %v", err)
	}
	if string(content) != "original" {
		t.Fatalf("existing final was overwritten: %q", content)
	}
}

func TestStoreAllowsUnsupportedDirectorySync(t *testing.T) {
	storage := newTestStorage(t)
	storage.hooks.syncDir = func(*os.File) error {
		return fmt.Errorf("directory sync: %w", errors.ErrUnsupported)
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("Store with unsupported directory sync: %v", err)
	}
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove with unsupported directory sync: %v", err)
	}
}

func TestCleanupTempsIsScopedAndRejectsSymlinks(t *testing.T) {
	storage := newTestStorage(t)
	for _, shareID := range []string{testShareID, otherShareID} {
		reservation, err := storage.Reserve(t.Context(), testOwnerID, shareID, 0)
		if err != nil {
			t.Fatalf("Reserve %s: %v", shareID, err)
		}
		reservation.Release()
		path := filepath.Join(filepath.Dir(storageTestSharePath(storage)), shareID, tempNamePrefix+strings.Repeat("a", 32))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("Write temp in %s: %v", shareID, err)
		}
	}

	removed, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID)
	if err != nil {
		t.Fatalf("CleanupTemps: %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupTemps removed %d, want 1", removed)
	}
	otherPath := filepath.Join(filepath.Dir(storageTestSharePath(storage)), otherShareID, tempNamePrefix+strings.Repeat("a", 32))
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("other share temp was touched: %v", err)
	}

	if runtime.GOOS != "windows" {
		symlinkName := tempNamePrefix + strings.Repeat("b", 32)
		if err := os.Symlink(otherPath, filepath.Join(storageTestSharePath(storage), symlinkName)); err != nil {
			t.Fatalf("Symlink temp: %v", err)
		}
		if _, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID); !errors.Is(err, ErrSymlink) {
			t.Fatalf("CleanupTemps symlink error = %v, want ErrSymlink", err)
		}
		if _, err := os.Lstat(filepath.Join(storageTestSharePath(storage), symlinkName)); err != nil {
			t.Fatalf("symlink temp was removed: %v", err)
		}
	}
}

func TestNewStorageRebuildsCommittedQuotaUntilExplicitRemoval(t *testing.T) {
	rootPath := t.TempDir()
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, strings.NewReader("abc"))
	if err != nil {
		_ = storage.Close()
		t.Fatalf("Store: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close first Storage: %v", err)
	}

	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := requireUsage(t, reopened); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("rebuilt usage = %+v", got)
	}
	if err := reopened.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove reopened file: %v", err)
	}
	if got := requireUsage(t, reopened); got != (QuotaUsage{}) {
		t.Fatalf("usage after explicit removal = %+v", got)
	}
}

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return storage
}

func requireUsage(t *testing.T, storage *Storage) QuotaUsage {
	t.Helper()
	usage, err := storage.Usage(t.Context(), testOwnerID, testShareID)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	return usage
}

func storageTestSharePath(storage *Storage) string {
	return filepath.Join(storage.root.Name(), testOwnerID, testShareID)
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("reader failed")
}

type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelAfterFirstRead) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	buffer[0] = 'x'
	r.cancel()
	return 1, nil
}
