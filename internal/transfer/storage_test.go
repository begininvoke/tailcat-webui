package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
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

func TestNewStorageRejectsConfiguredRootSwapDuringOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deterministic replacement of an open directory is not portable on Windows")
	}
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "staging")
	anchoredPath := filepath.Join(parent, "original")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatalf("Mkdir root: %v", err)
	}

	_, err := newStorage(rootPath, constructorHooks{afterInitialLstat: func() {
		if err := os.Rename(rootPath, anchoredPath); err != nil {
			t.Fatalf("rename initial root: %v", err)
		}
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatalf("create replacement root: %v", err)
		}
	}})
	if !errors.Is(err, ErrRootChanged) {
		t.Fatalf("root swap error = %v, want ErrRootChanged", err)
	}
}

func TestStorageRemainsAnchoredAfterConfiguredPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deterministic replacement of an open directory is not portable on Windows")
	}
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "staging")
	anchoredPath := filepath.Join(parent, "anchored")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := os.Rename(rootPath, anchoredPath); err != nil {
		t.Fatalf("rename configured root: %v", err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatalf("create replacement root: %v", err)
	}

	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store through anchored root: %v", err)
	}
	anchoredFile := filepath.Join(anchoredPath, testOwnerID, testShareID, stored.StorageName)
	if content, err := os.ReadFile(anchoredFile); err != nil || string(content) != "abc" {
		t.Fatalf("anchored file content=%q error=%v", content, err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatalf("ReadDir replacement root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement root was used: %v", entryNames(entries))
	}
}

func TestStorageRejectsUnsafeDirectoryAndFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go FileMode permission bits do not represent Windows ACLs")
	}
	t.Run("root", func(t *testing.T) {
		rootPath := t.TempDir()
		if err := os.Chmod(rootPath, 0o755); err != nil {
			t.Fatalf("Chmod root: %v", err)
		}
		if _, err := NewStorage(rootPath); !errors.Is(err, ErrPermissions) {
			t.Fatalf("permissive root error = %v, want ErrPermissions", err)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "owner", setup: func(t *testing.T, rootPath string) {
			if err := os.Mkdir(filepath.Join(rootPath, testOwnerID), 0o755); err != nil {
				t.Fatalf("Mkdir owner: %v", err)
			}
		}},
		{name: "share", setup: func(t *testing.T, rootPath string) {
			ownerPath := filepath.Join(rootPath, testOwnerID)
			if err := os.Mkdir(ownerPath, 0o700); err != nil {
				t.Fatalf("Mkdir owner: %v", err)
			}
			if err := os.Mkdir(filepath.Join(ownerPath, testShareID), 0o755); err != nil {
				t.Fatalf("Mkdir share: %v", err)
			}
		}},
		{name: "stored_file", setup: func(t *testing.T, rootPath string) {
			sharePath := makeStorageDirectories(t, rootPath)
			if err := os.WriteFile(filepath.Join(sharePath, strings.Repeat("a", 32)), []byte("x"), 0o644); err != nil {
				t.Fatalf("Write stored file: %v", err)
			}
		}},
		{name: "temporary_file", setup: func(t *testing.T, rootPath string) {
			sharePath := makeStorageDirectories(t, rootPath)
			if err := os.WriteFile(filepath.Join(sharePath, tempNamePrefix+strings.Repeat("a", 32)), []byte("x"), 0o644); err != nil {
				t.Fatalf("Write temporary file: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootPath := t.TempDir()
			if err := os.Chmod(rootPath, 0o700); err != nil {
				t.Fatalf("Chmod root: %v", err)
			}
			tc.setup(t, rootPath)
			if _, err := NewStorage(rootPath); !errors.Is(err, ErrPermissions) {
				t.Fatalf("NewStorage error = %v, want ErrPermissions", err)
			}
		})
	}
}

func TestStorageRejectsMultiplyLinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link count validation is not exposed by os.FileInfo on Windows")
	}
	rootPath := t.TempDir()
	sharePath := makeStorageDirectories(t, rootPath)
	firstName := strings.Repeat("a", 32)
	secondName := strings.Repeat("b", 32)
	if err := os.WriteFile(filepath.Join(sharePath, firstName), []byte("x"), 0o600); err != nil {
		t.Fatalf("Write stored file: %v", err)
	}
	if err := os.Link(filepath.Join(sharePath, firstName), filepath.Join(sharePath, secondName)); err != nil {
		t.Fatalf("Link stored file: %v", err)
	}
	if _, err := NewStorage(rootPath); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("restart scan hard-link error = %v, want ErrMultipleLinks", err)
	}

	cleanStorage := newTestStorage(t)
	reservation, err := cleanStorage.Reserve(t.Context(), testOwnerID, testShareID, 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()
	cleanShare := storageTestSharePath(cleanStorage)
	if err := os.WriteFile(filepath.Join(cleanShare, firstName), []byte("x"), 0o600); err != nil {
		t.Fatalf("Write live stored file: %v", err)
	}
	if err := os.Link(filepath.Join(cleanShare, firstName), filepath.Join(cleanShare, secondName)); err != nil {
		t.Fatalf("Link live stored file: %v", err)
	}
	if _, err := cleanStorage.Open(t.Context(), testOwnerID, testShareID, firstName); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("Open hard-link error = %v, want ErrMultipleLinks", err)
	}
	if err := cleanStorage.Remove(t.Context(), testOwnerID, testShareID, firstName); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("Remove hard-link error = %v, want ErrMultipleLinks", err)
	}
}

func TestStorageOperationsAfterCloseFailSafely(t *testing.T) {
	storage, err := NewStorage(filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	validName := strings.Repeat("a", 32)
	checks := []error{}
	_, err = storage.Reserve(t.Context(), testOwnerID, testShareID, 1)
	checks = append(checks, err)
	_, err = storage.Store(t.Context(), testOwnerID, testShareID, 1, readCloser(strings.NewReader("x")))
	checks = append(checks, err)
	_, err = storage.StoreReserved(t.Context(), reservation, readCloser(strings.NewReader("x")))
	checks = append(checks, err)
	_, err = storage.Usage(t.Context(), testOwnerID, testShareID)
	checks = append(checks, err)
	_, err = storage.Open(t.Context(), testOwnerID, testShareID, validName)
	checks = append(checks, err)
	checks = append(checks, storage.Remove(t.Context(), testOwnerID, testShareID, validName))
	_, err = storage.CleanupTemps(t.Context(), testOwnerID, testShareID)
	checks = append(checks, err)
	_, err = storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, validName, testFileID, "x")
	checks = append(checks, err)
	for index, err := range checks {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("operation %d error = %v, want ErrClosed", index, err)
		}
	}
	if got := reservation.state.Load(); got != reservationReleased {
		t.Fatalf("reservation state after closed StoreReserved = %d, want released", got)
	}
}

func TestCloseCancelsBlockedUploadAndWaitsForCleanup(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	reader := newBlockingReadCloser()
	storeDone := make(chan error, 1)
	go func() {
		_, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, reader)
		storeDone <- err
	}()
	<-reader.started
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-storeDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked Store error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		_ = reader.Close()
		<-storeDone
		t.Fatal("Close returned without canceling and joining blocked Store")
	}
	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	defer reopened.Close()
	if got := requireUsage(t, reopened); got != (QuotaUsage{}) {
		t.Fatalf("usage after Close cancellation = %+v", got)
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, testOwnerID, testShareID))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Close cancellation left entries: %v", entryNames(entries))
	}
}

func TestCloseWaitsForEnteredPublishAndPreventsPostCloseMutation(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	storage.hooks.beforePublish = func() {
		close(entered)
		<-release
	}
	storeDone := make(chan error, 1)
	go func() {
		_, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
		storeDone <- err
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- storage.Close() }()
	for !storage.closed.Load() {
		runtime.Gosched()
	}
	select {
	case err := <-closeDone:
		close(release)
		<-storeDone
		t.Fatalf("Close returned before entered Store exited: %v", err)
	default:
	}
	close(release)
	if err := <-storeDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("entered Store error = %v, want ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	defer reopened.Close()
	if got := requireUsage(t, reopened); got != (QuotaUsage{}) {
		t.Fatalf("usage after entered-operation Close = %+v", got)
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, testOwnerID, testShareID))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("post-Close publication entries: %v", entryNames(entries))
	}
}

func TestOpenReturnsIndependentReadOnlyHandleAcrossStorageClose(t *testing.T) {
	storage, err := NewStorage(filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	handle, err := storage.Open(t.Context(), testOwnerID, testShareID, stored.StorageName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := storage.Close(); err != nil {
		_ = handle.Close()
		t.Fatalf("Storage Close: %v", err)
	}
	content, err := io.ReadAll(handle)
	if err != nil {
		_ = handle.Close()
		t.Fatalf("Read after Storage Close: %v", err)
	}
	if string(content) != "abc" {
		t.Fatalf("content after Storage Close = %q", content)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("ReadHandle Close: %v", err)
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
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatalf("Chmod root: %v", err)
	}
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
	stored, err := storage.StoreReserved(t.Context(), reservation, readCloser(strings.NewReader("x")))
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
	storage.hooks.beforePublish = func() {
		close(entered)
		<-continueRename
	}

	type result struct {
		file StoredFile
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		file, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
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
		reader    func() io.ReadCloser
		configure func(*Storage)
	}{
		{name: "partial", ctx: context.Background, size: 4, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abc")) }},
		{name: "oversized", ctx: context.Background, size: 3, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abcd")) }},
		{name: "reader_error", ctx: context.Background, size: 3, reader: func() io.ReadCloser { return readCloser(io.MultiReader(strings.NewReader("a"), errorReader{})) }},
		{name: "canceled", ctx: canceledContext, size: 3, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abc")) }},
		{name: "file_sync", ctx: context.Background, size: 3, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abc")) }, configure: func(s *Storage) { s.hooks.syncFile = func(*os.File) error { return errors.New("sync failed") } }},
		{name: "link", ctx: context.Background, size: 3, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abc")) }, configure: func(s *Storage) {
			s.hooks.link = func(*os.Root, string, string) error { return errors.New("link failed") }
		}},
		{name: "directory_sync", ctx: context.Background, size: 3, reader: func() io.ReadCloser { return readCloser(strings.NewReader("abc")) }, configure: func(s *Storage) {
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
	if !reader.closed.Load() {
		t.Fatal("partially read source was not closed")
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

func TestStoreOverrunProbeNeverWritesOrHashesProbeByte(t *testing.T) {
	storage := newTestStorage(t)
	var observedSize int64
	var observedHash string
	storage.hooks.afterIngest = func(size int64, hash string) {
		observedSize = size
		observedHash = hash
	}
	reader := &recordingReadCloser{reader: strings.NewReader("abcd")}
	if _, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, reader); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("one-byte overrun error = %v, want ErrSizeMismatch", err)
	}
	if observedSize != 3 {
		t.Fatalf("temporary size after overrun = %d, want 3", observedSize)
	}
	if observedHash != "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85" {
		t.Fatalf("temporary hash after overrun = %q, want abc vector", observedHash)
	}
	if got := reader.readSizes; !reflect.DeepEqual(got, []int{3, 1}) {
		t.Fatalf("reader buffer sizes = %v, want exact copy then one-byte probe", got)
	}
	if !reader.closed.Load() {
		t.Fatal("overrun reader was not closed")
	}
}

func TestStoreCancellationClosesBlockedReaderAndJoins(t *testing.T) {
	storage := newTestStorage(t)
	ctx, cancel := context.WithCancel(t.Context())
	reader := newBlockingReadCloser()
	done := make(chan error, 1)
	go func() {
		_, err := storage.Store(ctx, testOwnerID, testShareID, 1, reader)
		done <- err
	}()
	<-reader.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked cancellation error = %v, want context.Canceled", err)
	}
	if !reader.closed.Load() {
		t.Fatal("blocked reader Close was not observed")
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after blocked cancellation = %+v", got)
	}
}

func TestStoreCancellationUnblocksPipeBody(t *testing.T) {
	storage := newTestStorage(t)
	pipeReader, pipeWriter := io.Pipe()
	t.Cleanup(func() { _ = pipeWriter.Close() })
	reader := &signalingReadCloser{ReadCloser: pipeReader, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := storage.Store(ctx, testOwnerID, testShareID, 1, reader)
		done <- err
	}()
	<-reader.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("pipe cancellation error = %v, want context.Canceled", err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after pipe cancellation = %+v", got)
	}
}

func TestStoreRejectsPathologicalNoProgressAndClosesReader(t *testing.T) {
	storage := newTestStorage(t)
	reader := &noProgressReadCloser{}
	if _, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, reader); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("no-progress error = %v, want io.ErrNoProgress", err)
	}
	if reader.reads.Load() > maxConsecutiveEmptyReads {
		t.Fatalf("no-progress reads = %d, max %d", reader.reads.Load(), maxConsecutiveEmptyReads)
	}
	if !reader.closed.Load() {
		t.Fatal("no-progress reader was not closed")
	}
}

func TestStoreClosesReaderOnSuccessAndAdmissionFailure(t *testing.T) {
	storage := newTestStorage(t)
	success := &recordingReadCloser{reader: strings.NewReader("x")}
	if _, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, success); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !success.closed.Load() {
		t.Fatal("successful reader was not closed")
	}
	rejected := &recordingReadCloser{reader: strings.NewReader("x")}
	if _, err := storage.Store(t.Context(), testOwnerID, testShareID, MaxFileBytes+1, rejected); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("admission error = %v, want ErrFileTooLarge", err)
	}
	if !rejected.closed.Load() {
		t.Fatal("admission-rejected reader was not closed")
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

	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, readCloser(strings.NewReader("x")))
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

func TestStoreRejectsSymlinkFinalNameCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	storage := newTestStorage(t)
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()
	finalName := strings.Repeat("01", randomNameBytes)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("Write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(storageTestSharePath(storage), finalName)); err != nil {
		t.Fatalf("Symlink final collision: %v", err)
	}
	storage.random = bytes.NewReader(append(bytes.Repeat([]byte{0}, randomNameBytes), bytes.Repeat([]byte{1}, randomNameBytes)...))
	if _, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, readCloser(strings.NewReader("x"))); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink final collision error = %v, want ErrSymlink", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "untouched" {
		t.Fatalf("symlink target content=%q error=%v", content, err)
	}
}

func TestIndependentStorageHandlesPublishNoReplace(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	defer first.Close()
	second, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage second: %v", err)
	}
	defer second.Close()

	finalBytes := bytes.Repeat([]byte{0xff}, randomNameBytes)
	first.random = bytes.NewReader(append(bytes.Repeat([]byte{0x01}, randomNameBytes), finalBytes...))
	second.random = bytes.NewReader(append(bytes.Repeat([]byte{0x02}, randomNameBytes), finalBytes...))
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	barrier := func() {
		ready <- struct{}{}
		<-start
	}
	first.hooks.beforePublish = barrier
	second.hooks.beforePublish = barrier

	type result struct {
		storage *Storage
		content string
		file    StoredFile
		err     error
	}
	results := make(chan result, 2)
	go func() {
		file, err := first.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("one")))
		results <- result{storage: first, content: "one", file: file, err: err}
	}()
	go func() {
		file, err := second.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("two")))
		results <- result{storage: second, content: "two", file: file, err: err}
	}()
	<-ready
	<-ready
	close(start)
	firstResult := <-results
	secondResult := <-results

	all := []result{firstResult, secondResult}
	var winner *result
	for index := range all {
		if all[index].err == nil {
			if winner != nil {
				t.Fatal("both independent Storage handles published the same final name")
			}
			winner = &all[index]
		}
	}
	if winner == nil {
		t.Fatalf("both publications failed: %v, %v", firstResult.err, secondResult.err)
	}
	wantName := strings.Repeat("ff", randomNameBytes)
	if winner.file.StorageName != wantName {
		t.Fatalf("winner storage name = %q, want %q", winner.file.StorageName, wantName)
	}
	content, err := os.ReadFile(filepath.Join(rootPath, testOwnerID, testShareID, wantName))
	if err != nil {
		t.Fatalf("ReadFile winner: %v", err)
	}
	if string(content) != winner.content {
		t.Fatalf("winner content = %q, want %q", content, winner.content)
	}
	for index := range all {
		usage, err := all[index].storage.Usage(t.Context(), testOwnerID, testShareID)
		if err != nil {
			t.Fatalf("Usage: %v", err)
		}
		if usage != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
			t.Fatalf("shared usage from handle %d = %+v", index, usage)
		}
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, testOwnerID, testShareID))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if got := entryNames(entries); len(got) != 1 || got[0] != wantName {
		t.Fatalf("publication entries = %v, want only %q", got, wantName)
	}
}

func TestIndependentStorageHandlesShareQuotaAdmission(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	defer first.Close()
	second, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage second: %v", err)
	}
	defer second.Close()
	firstReservation, err := first.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	defer firstReservation.Release()
	secondReservation, err := second.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	defer secondReservation.Release()
	if _, err := first.Reserve(t.Context(), testOwnerID, testShareID, 1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("cross-handle over-share error = %v, want ErrQuotaExceeded", err)
	}
	if got := requireUsage(t, second); got != (QuotaUsage{OwnerBytes: MaxShareBytes, ShareBytes: MaxShareBytes, ShareFiles: 2}) {
		t.Fatalf("shared usage = %+v", got)
	}
}

func TestIndependentStorageHandlesShareFileCountQuota(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	defer first.Close()
	second, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage second: %v", err)
	}
	defer second.Close()
	reservations := make([]*Reservation, 0, MaxFilesPerShare)
	for index := range MaxFilesPerShare {
		storage := first
		if index%2 == 1 {
			storage = second
		}
		reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0)
		if err != nil {
			t.Fatalf("Reserve file %d: %v", index, err)
		}
		reservations = append(reservations, reservation)
	}
	if _, err := second.Reserve(t.Context(), testOwnerID, testShareID, 0); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("cross-handle file-count error = %v, want ErrQuotaExceeded", err)
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
}

func TestIndependentStorageHandlesShareConcurrentOwnerQuota(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	defer first.Close()
	second, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage second: %v", err)
	}
	defer second.Close()

	const attempts = 32
	start := make(chan struct{})
	reservations := make(chan *Reservation, attempts)
	errorsCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for index := range attempts {
		storage := first
		if index%2 == 1 {
			storage = second
		}
		shareID := fmt.Sprintf("01900000-0000-7000-8000-%012d", index+100)
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
	admitted := 0
	for reservation := range reservations {
		admitted++
		reservation.Release()
	}
	if want := int(MaxOwnerBytes / (MaxFileBytes / 2)); admitted != want {
		t.Fatalf("cross-handle admitted %d, want %d", admitted, want)
	}
	for err := range errorsCh {
		if !errors.Is(err, ErrQuotaExceeded) {
			t.Errorf("rejection error = %v, want ErrQuotaExceeded", err)
		}
	}
}

func TestIndependentStorageHandlesShareCommitRemovalAndCloseLifecycle(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	second, err := NewStorage(rootPath)
	if err != nil {
		_ = first.Close()
		t.Fatalf("NewStorage second: %v", err)
	}
	stored, err := first.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := requireUsage(t, second); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("second-handle committed usage = %+v", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	if got := requireUsage(t, second); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("usage after first close = %+v", got)
	}
	if err := second.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}

	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	defer reopened.Close()
	if got := requireUsage(t, reopened); got != (QuotaUsage{}) {
		t.Fatalf("reopened usage after shared removal = %+v", got)
	}
}

func TestSharedQuotaConcurrentCloseAndRestart(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	handles := make([]*Storage, 8)
	for index := range handles {
		storage, err := NewStorage(rootPath)
		if err != nil {
			t.Fatalf("NewStorage %d: %v", index, err)
		}
		handles[index] = storage
	}
	stored, err := handles[0].Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(handles))
	for _, storage := range handles {
		wg.Go(func() { errorsCh <- storage.Close() })
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	}
	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	defer reopened.Close()
	if got := requireUsage(t, reopened); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("restarted usage after concurrent close = %+v", got)
	}
	if err := reopened.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestClosingOneHandleReleasesItsPendingSharedReservations(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	second, err := NewStorage(rootPath)
	if err != nil {
		_ = first.Close()
		t.Fatalf("NewStorage second: %v", err)
	}
	defer second.Close()
	reservation, err := first.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	if got := reservation.state.Load(); got != reservationReleased {
		t.Fatalf("reservation state after owner Close = %d, want released", got)
	}
	if got := requireUsage(t, second); got != (QuotaUsage{}) {
		t.Fatalf("shared usage after reservation-owner Close = %+v", got)
	}
}

func TestRootIdentityQuotaRegistryDoesNotMergeReplacementDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deterministic replacement of an open directory is not portable on Windows")
	}
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "staging")
	anchoredPath := filepath.Join(parent, "anchored")
	first, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage first: %v", err)
	}
	defer first.Close()
	if err := os.Rename(rootPath, anchoredPath); err != nil {
		t.Fatalf("Rename root: %v", err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatalf("Mkdir replacement: %v", err)
	}
	second, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage replacement: %v", err)
	}
	defer second.Close()

	reservations := make([]*Reservation, 0, 2)
	for range 2 {
		reservation, err := first.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
		if err != nil {
			t.Fatalf("first-root Reserve: %v", err)
		}
		reservations = append(reservations, reservation)
	}
	defer func() {
		for _, reservation := range reservations {
			reservation.Release()
		}
	}()
	separate, err := second.Reserve(t.Context(), testOwnerID, testShareID, MaxFileBytes)
	if err != nil {
		t.Fatalf("replacement root was incorrectly quota-merged: %v", err)
	}
	separate.Release()
}

func TestPostPublishRollbackFailureRetainsQuotaAndReturnsCleanupName(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	finalName := strings.Repeat("02", randomNameBytes)
	storage.random = bytes.NewReader(append(bytes.Repeat([]byte{0x01}, randomNameBytes), bytes.Repeat([]byte{0x02}, randomNameBytes)...))
	storage.hooks.syncDir = func(*os.File) error { return errors.New("sync failed") }
	storage.hooks.remove = func(root *os.Root, name string) error {
		if name == finalName {
			return errors.New("rollback removal failed")
		}
		return root.Remove(name)
	}

	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err == nil {
		t.Fatal("Store unexpectedly succeeded")
	}
	if stored.StorageName != finalName {
		t.Fatalf("cleanup storage name = %q, want %q", stored.StorageName, finalName)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("usage after incomplete rollback = %+v", got)
	}
	if content, readErr := os.ReadFile(filepath.Join(rootPath, testOwnerID, testShareID, finalName)); readErr != nil || string(content) != "abc" {
		t.Fatalf("retained final content=%q error=%v", content, readErr)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := requireUsage(t, reopened); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("reconciled usage = %+v", got)
	}
	if err := reopened.Remove(t.Context(), testOwnerID, testShareID, finalName); err != nil {
		t.Fatalf("Remove retained final: %v", err)
	}
	if err := reopened.Remove(t.Context(), testOwnerID, testShareID, finalName); err != nil {
		t.Fatalf("idempotent second Remove: %v", err)
	}
	if got := requireUsage(t, reopened); got != (QuotaUsage{}) {
		t.Fatalf("usage after cleanup = %+v", got)
	}
}

func TestCleanupTempsRecoversVerifiedPublishedPairAfterPersistentUnlinkFailure(t *testing.T) {
	storage, _, tempName, finalName := makeFailedTempFinalPair(t)
	if _, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID); err == nil {
		t.Fatal("CleanupTemps unexpectedly bypassed persistent unlink failure")
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("usage during persistent unlink failure = %+v", got)
	}
	for _, name := range []string{tempName, finalName} {
		if _, err := os.Lstat(filepath.Join(storageTestSharePath(storage), name)); err != nil {
			t.Fatalf("pair entry %q missing after failed cleanup: %v", name, err)
		}
	}

	storage.hooks.remove = (*os.Root).Remove
	removed, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID)
	if err != nil {
		t.Fatalf("CleanupTemps recovery: %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupTemps removed %d entries, want temp alias only", removed)
	}
	if _, err := os.Lstat(filepath.Join(storageTestSharePath(storage), tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp alias still exists: %v", err)
	}
	handle, err := storage.Open(t.Context(), testOwnerID, testShareID, finalName)
	if err != nil {
		t.Fatalf("Open recovered final: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close recovered final: %v", err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("usage after pair recovery = %+v", got)
	}
}

func TestRestartRecoversVerifiedTempFinalPairAndChargesOnce(t *testing.T) {
	storage, rootPath, tempName, finalName := makeFailedTempFinalPair(t)
	if err := storage.Close(); err != nil {
		t.Fatalf("Close failed Storage: %v", err)
	}
	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage recovery: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := os.Lstat(filepath.Join(rootPath, testOwnerID, testShareID, tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart left temp alias: %v", err)
	}
	if got := requireUsage(t, reopened); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("restart pair usage = %+v", got)
	}
	if err := reopened.Remove(t.Context(), testOwnerID, testShareID, finalName); err != nil {
		t.Fatalf("Remove recovered final: %v", err)
	}
}

func TestRemoveRecoversVerifiedTempFinalPairThenRemovesBoth(t *testing.T) {
	storage, _, tempName, finalName := makeFailedTempFinalPair(t)
	storage.hooks.remove = (*os.Root).Remove
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, finalName); err != nil {
		t.Fatalf("Remove pair: %v", err)
	}
	for _, name := range []string{tempName, finalName} {
		if _, err := os.Lstat(filepath.Join(storageTestSharePath(storage), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pair entry %q remains: %v", name, err)
		}
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after pair removal = %+v", got)
	}
}

func TestRemoveTempAliasRoleMismatchDoesNotLeakVerifiedFinal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc/self/fd")
	}
	storage, _, tempName, finalName := makeFailedTempFinalPair(t)
	finalPath := filepath.Join(storageTestSharePath(storage), finalName)
	baseline := countOpenDescriptorsForFile(t, finalPath)
	previousGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGCPercent)

	for range 32 {
		if err := storage.Remove(t.Context(), testOwnerID, testShareID, tempName); !errors.Is(err, ErrMultipleLinks) {
			t.Fatalf("Remove temp alias error = %v, want ErrMultipleLinks", err)
		}
	}
	storage.hooks.remove = (*os.Root).Remove
	removed, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID)
	if err != nil {
		t.Fatalf("CleanupTemps: %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupTemps removed %d entries, want temp alias only", removed)
	}
	if got := countOpenDescriptorsForFile(t, finalPath); got != baseline {
		t.Fatalf("open descriptors for verified final = %d, want baseline %d", got, baseline)
	}
}

func TestVerifiedPairCallersJoinCloseErrorOnRoleMismatch(t *testing.T) {
	closeFailure := errors.New("close verified final failed")
	for _, tc := range []struct {
		name string
		call func(*testing.T, *Storage, string, string) error
	}{
		{
			name: "remove_temp_alias",
			call: func(t *testing.T, storage *Storage, tempName, _ string) error {
				t.Helper()
				return storage.Remove(t.Context(), testOwnerID, testShareID, tempName)
			},
		},
		{
			name: "cleanup_recovery_with_final_alias",
			call: func(t *testing.T, storage *Storage, _, finalName string) error {
				t.Helper()
				shareRoot, err := storage.openShare(testOwnerID, testShareID, false)
				if err != nil {
					t.Fatalf("openShare: %v", err)
				}
				_, recoverErr := storage.recoverTempAlias(shareRoot, finalName)
				return errors.Join(recoverErr, shareRoot.Close())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, _, tempName, finalName := makeFailedTempFinalPair(t)
			closeCalls := 0
			storage.hooks.closeVerifiedFinal = func(file *os.File) error {
				closeCalls++
				return errors.Join(file.Close(), closeFailure)
			}
			for range 16 {
				err := tc.call(t, storage, tempName, finalName)
				if !errors.Is(err, ErrMultipleLinks) || !errors.Is(err, closeFailure) {
					t.Fatalf("role-mismatch error = %v, want ErrMultipleLinks joined with close failure", err)
				}
			}
			if closeCalls != 16 {
				t.Fatalf("verified final close calls = %d, want 16", closeCalls)
			}
		})
	}
}

func TestRecoveryRejectsAmbiguousThreeLinkPair(t *testing.T) {
	storage := newTestStorage(t)
	reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reservation.Release()
	sharePath := storageTestSharePath(storage)
	tempName := tempNamePrefix + strings.Repeat("a", 32)
	finalName := strings.Repeat("b", 32)
	thirdName := strings.Repeat("c", 32)
	if err := os.WriteFile(filepath.Join(sharePath, tempName), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, name := range []string{finalName, thirdName} {
		if err := os.Link(filepath.Join(sharePath, tempName), filepath.Join(sharePath, name)); err != nil {
			t.Fatalf("Link %s: %v", name, err)
		}
	}
	if _, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("ambiguous cleanup error = %v, want ErrMultipleLinks", err)
	}
}

func TestPairRecoveryRejectsFinalReplacementBetweenVerifyAndUnlink(t *testing.T) {
	storage, _, tempName, finalName := makeFailedTempFinalPair(t)
	storage.hooks.remove = (*os.Root).Remove
	var replaced atomic.Bool
	storage.hooks.afterPairVerified = func() {
		if !replaced.CompareAndSwap(false, true) {
			return
		}
		finalPath := filepath.Join(storageTestSharePath(storage), finalName)
		if err := os.Remove(finalPath); err != nil {
			t.Errorf("remove verified final: %v", err)
			return
		}
		if err := os.WriteFile(finalPath, []byte("replacement"), 0o600); err != nil {
			t.Errorf("write replacement final: %v", err)
		}
	}
	if _, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("replacement recovery error = %v, want ErrFileChanged", err)
	}
	if _, err := os.Lstat(filepath.Join(storageTestSharePath(storage), tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified temp alias remains: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(storageTestSharePath(storage), finalName))
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement content=%q error=%v", content, err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("quota after rejected replacement = %+v", got)
	}
}

func TestValidateStableFileIdentityRejectsReplacement(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first")
	secondPath := filepath.Join(directory, "second")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	first, err := os.Stat(firstPath)
	if err != nil {
		t.Fatalf("Stat first: %v", err)
	}
	second, err := os.Stat(secondPath)
	if err != nil {
		t.Fatalf("Stat second: %v", err)
	}
	if err := validateStableFileIdentity(first, first, first); err != nil {
		t.Fatalf("stable identity: %v", err)
	}
	if err := validateStableFileIdentity(first, first, second); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("replacement identity error = %v, want ErrFileChanged", err)
	}
}

func TestCloseRacingAfterLifecycleCheckWaitsForLinkWinner(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	storage.hooks.afterLifecycleCheckBeforeLink = func() {
		close(entered)
		<-release
	}
	storeDone := make(chan struct {
		file StoredFile
		err  error
	}, 1)
	go func() {
		file, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
		storeDone <- struct {
			file StoredFile
			err  error
		}{file: file, err: err}
	}()
	<-entered
	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- storage.Close()
	}()
	<-closeStarted
	select {
	case err := <-closeDone:
		close(release)
		<-storeDone
		t.Fatalf("Close crossed the checked-but-unlinked publication: %v", err)
	default:
	}
	close(release)
	stored := <-storeDone
	if stored.err != nil {
		t.Fatalf("link-winning Store: %v", stored.err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("reopen Storage: %v", err)
	}
	defer reopened.Close()
	if got := requireUsage(t, reopened); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("post-Close committed usage = %+v", got)
	}
	handle, err := reopened.Open(t.Context(), testOwnerID, testShareID, stored.file.StorageName)
	if err != nil {
		t.Fatalf("Open linked winner: %v", err)
	}
	content, err := io.ReadAll(handle)
	closeErr := handle.Close()
	if err != nil || closeErr != nil || string(content) != "abc" {
		t.Fatalf("linked winner content=%q readErr=%v closeErr=%v", content, err, closeErr)
	}
}

func TestRemoveMissingCommittedFileReleasesQuotaIdempotently(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := os.Remove(filepath.Join(storageTestSharePath(storage), stored.StorageName)); err != nil {
		t.Fatalf("external Remove: %v", err)
	}
	for range 2 {
		if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
			t.Fatalf("Remove missing committed file: %v", err)
		}
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after missing-file reconciliation = %+v", got)
	}
}

func TestRemoveMissingShareReleasesMatchingCommittedQuota(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := os.RemoveAll(storageTestSharePath(storage)); err != nil {
		t.Fatalf("RemoveAll share fixture: %v", err)
	}
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove missing share: %v", err)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{}) {
		t.Fatalf("usage after missing-share reconciliation = %+v", got)
	}
}

func TestRemoveErrorRetainsCommittedQuota(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	storage.hooks.remove = func(*os.Root, string) error { return errors.New("remove failed") }
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err == nil {
		t.Fatal("Remove unexpectedly succeeded")
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("usage after remove error = %+v", got)
	}
	storage.hooks.remove = (*os.Root).Remove
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("cleanup Remove: %v", err)
	}
}

func TestStoreAllowsUnsupportedDirectorySync(t *testing.T) {
	storage := newTestStorage(t)
	storage.hooks.syncDir = func(*os.File) error {
		return fmt.Errorf("directory sync: %w", errors.ErrUnsupported)
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, readCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatalf("Store with unsupported directory sync: %v", err)
	}
	if err := storage.Remove(t.Context(), testOwnerID, testShareID, stored.StorageName); err != nil {
		t.Fatalf("Remove with unsupported directory sync: %v", err)
	}
}

func TestDirectorySyncErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unsupported", err: errors.ErrUnsupported, want: true},
		{name: "wrapped_unsupported", err: fmt.Errorf("sync: %w", errors.ErrUnsupported), want: true},
		{name: "invalid", err: fs.ErrInvalid, want: true},
		{name: "wrapped_invalid", err: fmt.Errorf("sync: %w", fs.ErrInvalid), want: true},
		{name: "io_error", err: errors.New("I/O failure"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnsupportedDirectorySync(tc.err); got != tc.want {
				t.Fatalf("isUnsupportedDirectorySync(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
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

func TestCleanupTempsRejectsUnsafeModesAndHardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go FileMode and hard-link count do not represent Windows ACL/link state")
	}
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string, string)
		want  error
	}{
		{name: "permissive", want: ErrPermissions, setup: func(t *testing.T, sharePath, tempName string) {
			if err := os.WriteFile(filepath.Join(sharePath, tempName), []byte("x"), 0o644); err != nil {
				t.Fatalf("WriteFile permissive temp: %v", err)
			}
		}},
		{name: "hard_link", want: ErrMultipleLinks, setup: func(t *testing.T, sharePath, tempName string) {
			first := filepath.Join(sharePath, tempName)
			if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
				t.Fatalf("WriteFile temp: %v", err)
			}
			if err := os.Link(first, filepath.Join(sharePath, tempNamePrefix+strings.Repeat("f", 32))); err != nil {
				t.Fatalf("Link temp: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := newTestStorage(t)
			reservation, err := storage.Reserve(t.Context(), testOwnerID, testShareID, 0)
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			reservation.Release()
			tempName := tempNamePrefix + strings.Repeat("e", 32)
			tc.setup(t, storageTestSharePath(storage), tempName)
			if _, err := storage.CleanupTemps(t.Context(), testOwnerID, testShareID); !errors.Is(err, tc.want) {
				t.Fatalf("CleanupTemps error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewStorageRebuildsCommittedQuotaUntilExplicitRemoval(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
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
	storage, err := NewStorage(filepath.Join(t.TempDir(), "staging"))
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

func countOpenDescriptorsForFile(t *testing.T, path string) int {
	t.Helper()
	target, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat descriptor target: %v", err)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("ReadDir /proc/self/fd: %v", err)
	}
	count := 0
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("Stat process descriptor %q: %v", entry.Name(), err)
		}
		if os.SameFile(target, info) {
			count++
		}
	}
	return count
}

func makeStorageDirectories(t *testing.T, rootPath string) string {
	t.Helper()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatalf("Chmod root: %v", err)
	}
	ownerPath := filepath.Join(rootPath, testOwnerID)
	sharePath := filepath.Join(ownerPath, testShareID)
	if err := os.Mkdir(ownerPath, 0o700); err != nil {
		t.Fatalf("Mkdir owner: %v", err)
	}
	if err := os.Mkdir(sharePath, 0o700); err != nil {
		t.Fatalf("Mkdir share: %v", err)
	}
	return sharePath
}

func makeFailedTempFinalPair(t *testing.T) (*Storage, string, string, string) {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	tempName := tempNamePrefix + strings.Repeat("01", randomNameBytes)
	finalName := strings.Repeat("02", randomNameBytes)
	storage.random = bytes.NewReader(append(bytes.Repeat([]byte{0x01}, randomNameBytes), bytes.Repeat([]byte{0x02}, randomNameBytes)...))
	storage.hooks.remove = func(_ *os.Root, name string) error {
		if name == tempName || name == finalName {
			return errors.New("persistent unlink failure")
		}
		return errors.New("unexpected unlink target")
	}
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err == nil {
		t.Fatal("Store unexpectedly succeeded")
	}
	if stored.StorageName != finalName {
		t.Fatalf("retained storage name = %q, want %q", stored.StorageName, finalName)
	}
	if got := requireUsage(t, storage); got != (QuotaUsage{OwnerBytes: 3, ShareBytes: 3, ShareFiles: 1}) {
		t.Fatalf("retained pair usage = %+v", got)
	}
	return storage, rootPath, tempName, finalName
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func readCloser(reader io.Reader) io.ReadCloser {
	return io.NopCloser(reader)
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
	closed atomic.Bool
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

func (r *cancelAfterFirstRead) Close() error {
	r.closed.Store(true)
	return nil
}

type recordingReadCloser struct {
	reader    io.Reader
	readSizes []int
	closed    atomic.Bool
}

func (r *recordingReadCloser) Read(buffer []byte) (int, error) {
	r.readSizes = append(r.readSizes, len(buffer))
	return r.reader.Read(buffer)
}

func (r *recordingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

type blockingReadCloser struct {
	started   chan struct{}
	unblocked chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	closed    atomic.Bool
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{started: make(chan struct{}), unblocked: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.unblocked
	return 0, errors.New("reader closed")
}

func (r *blockingReadCloser) Close() error {
	r.closed.Store(true)
	r.closeOnce.Do(func() { close(r.unblocked) })
	return nil
}

type noProgressReadCloser struct {
	reads  atomic.Int32
	closed atomic.Bool
}

func ExampleStorage_Close_reentrantCallbacks() {
	fmt.Println("call Storage.Close only after source and hook callbacks return")
	// Output: call Storage.Close only after source and hook callbacks return
}

type signalingReadCloser struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (r *signalingReadCloser) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(buffer)
}

func (r *noProgressReadCloser) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, nil
}

func (r *noProgressReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}
