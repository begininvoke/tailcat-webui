package transfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/ca-x/tailcat-webui/ent/runtime"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
)

const testFileID = "01900000-0000-7000-8000-000000000004"

func TestBuildFileManifestEmptyKnownVectorAndImmutability(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 0, readCloser(strings.NewReader("")))
	if err != nil {
		t.Fatalf("Store empty: %v", err)
	}
	fileManifest, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "empty.txt")
	if err != nil {
		t.Fatalf("BuildFileManifest: %v", err)
	}
	if fileManifest.FileID() != testFileID || fileManifest.VirtualPath() != "empty.txt" || fileManifest.Size() != 0 {
		t.Fatalf("empty identity = %q %q %d", fileManifest.FileID(), fileManifest.VirtualPath(), fileManifest.Size())
	}
	if fileManifest.MTime().Location() != time.UTC {
		t.Fatalf("mtime location = %v, want UTC", fileManifest.MTime().Location())
	}
	if fileManifest.BLAKE3() != "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262" {
		t.Fatalf("empty BLAKE3 = %q", fileManifest.BLAKE3())
	}
	if fileManifest.BlockSize() != BlockSize || len(fileManifest.Blocks()) != 0 {
		t.Fatalf("empty layout block_size=%d blocks=%d", fileManifest.BlockSize(), len(fileManifest.Blocks()))
	}

	manifest := NewManifest(fileManifest)
	files := manifest.Files()
	if len(files) != 1 {
		t.Fatalf("manifest files = %d, want 1", len(files))
	}
	files[0] = FileManifest{}
	if manifest.Files()[0].FileID() != testFileID {
		t.Fatal("Manifest.Files exposed mutable backing storage")
	}
}

func TestBuildFileManifestExactAndOverBlockBoundariesAreDeterministic(t *testing.T) {
	storage := newTestStorage(t)
	for _, tc := range []struct {
		name       string
		size       int
		blockSizes []int64
	}{
		{name: "exact", size: int(BlockSize), blockSizes: []int64{BlockSize}},
		{name: "over", size: int(BlockSize + 1), blockSizes: []int64{BlockSize, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := bytes.Repeat([]byte{0x5a}, tc.size)
			stored, err := storage.Store(t.Context(), testOwnerID, testShareID, int64(len(content)), readCloser(bytes.NewReader(content)))
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			first, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "folder/data.bin")
			if err != nil {
				t.Fatalf("first BuildFileManifest: %v", err)
			}
			second, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "folder/data.bin")
			if err != nil {
				t.Fatalf("second BuildFileManifest: %v", err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatal("repeated manifest construction was nondeterministic")
			}
			if first.BLAKE3() != stored.BLAKE3 {
				t.Fatalf("manifest whole hash = %q, Store hash = %q", first.BLAKE3(), stored.BLAKE3)
			}
			blocks := first.Blocks()
			if len(blocks) != len(tc.blockSizes) {
				t.Fatalf("blocks = %d, want %d", len(blocks), len(tc.blockSizes))
			}
			for index, block := range blocks {
				if block.Index() != index || block.Offset() != int64(index)*BlockSize || block.Size() != tc.blockSizes[index] || len(block.BLAKE3()) != 64 {
					t.Errorf("block %d = index:%d offset:%d size:%d hash:%q", index, block.Index(), block.Offset(), block.Size(), block.BLAKE3())
				}
			}
			if len(blocks) > 0 {
				blocks[0] = Block{}
				if first.Blocks()[0].Size() == 0 {
					t.Fatal("FileManifest.Blocks exposed mutable backing storage")
				}
			}
		})
	}
}

func TestBuildFileManifestRejectsUnsafeIdentityAndOverFileLimit(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 1, readCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	for _, virtualPath := range []string{
		"目录/报告.txt",
		strings.Repeat("a/", 31) + "a",
		strings.Repeat("a", 1024),
	} {
		if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, virtualPath); err != nil {
			t.Errorf("valid boundary virtual path %q: %v", virtualPath, err)
		}
	}
	unsafePaths := []string{
		"", ".", "../x", "/x", "folder//x", "folder/./x", "folder/../x",
		`C:\x`, `\\?\C:\x`, "CON", "folder/con.txt", "folder/trailing. ",
		"folder/next\u0085line.txt",
		strings.Repeat("a", 1025), strings.Repeat("a/", 32) + "x",
	}
	for _, virtualPath := range unsafePaths {
		if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, virtualPath); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("virtual path %q error = %v, want ErrInvalidPath", virtualPath, err)
		}
	}
	if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, "not-a-file-id", "x"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid file ID error = %v, want ErrInvalidPath", err)
	}

	overName := strings.Repeat("e", 32)
	overPath := filepath.Join(storageTestSharePath(storage), overName)
	file, err := os.OpenFile(overPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("OpenFile oversized sparse fixture: %v", err)
	}
	if err := file.Truncate(MaxFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate oversized sparse fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close oversized sparse fixture: %v", err)
	}
	if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, overName, testFileID, "large.bin"); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized file error = %v, want ErrFileTooLarge", err)
	}
}

func TestBuildFileManifestBoundsWorkersAndStopsOnCancellation(t *testing.T) {
	storage := newTestStorage(t)
	content := bytes.Repeat([]byte("worker-data"), int(3*BlockSize)/len("worker-data")+1)
	content = content[:3*BlockSize]
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, int64(len(content)), readCloser(bytes.NewReader(content)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	var started atomic.Int32
	var stopped atomic.Int32
	storage.manifestHooks.workerStarted = func() { started.Add(1) }
	storage.manifestHooks.workerStopped = func() { stopped.Add(1) }
	fileManifest, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "workers.bin")
	if err != nil {
		t.Fatalf("BuildFileManifest: %v", err)
	}
	wantWorkers := min(runtime.GOMAXPROCS(0), 4)
	wantWorkers = max(wantWorkers, 1)
	if started.Load() != int32(wantWorkers) || stopped.Load() != int32(wantWorkers) {
		t.Fatalf("worker lifecycle started=%d stopped=%d want=%d", started.Load(), stopped.Load(), wantWorkers)
	}
	if len(fileManifest.Blocks()) != 3 {
		t.Fatalf("blocks = %d, want 3", len(fileManifest.Blocks()))
	}

	started.Store(0)
	stopped.Store(0)
	ctx, cancel := context.WithCancel(t.Context())
	storage.manifestHooks.afterReadBlock = func(index int) {
		if index == 0 {
			cancel()
		}
	}
	if _, err := storage.BuildFileManifest(ctx, testOwnerID, testShareID, stored.StorageName, testFileID, "workers.bin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled manifest error = %v, want context.Canceled", err)
	}
	if started.Load() != int32(wantWorkers) || stopped.Load() != int32(wantWorkers) {
		t.Fatalf("canceled worker lifecycle started=%d stopped=%d want=%d", started.Load(), stopped.Load(), wantWorkers)
	}
}

func TestBuildFileManifestDetectsFileChangingDuringHash(t *testing.T) {
	storage := newTestStorage(t)
	content := bytes.Repeat([]byte{0x42}, int(BlockSize+1))
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, int64(len(content)), readCloser(bytes.NewReader(content)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	var changed atomic.Bool
	storage.manifestHooks.afterReadBlock = func(index int) {
		if index != 0 || !changed.CompareAndSwap(false, true) {
			return
		}
		file, err := os.OpenFile(filepath.Join(storageTestSharePath(storage), stored.StorageName), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Errorf("OpenFile for mutation: %v", err)
			return
		}
		if _, err := file.Write([]byte("changed")); err != nil {
			t.Errorf("mutate file: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Errorf("close mutation file: %v", err)
		}
	}
	if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "changing.bin"); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("changed file error = %v, want ErrFileChanged", err)
	}
}

func TestBuildFileManifestDetectsMTimeChangingDuringHash(t *testing.T) {
	storage := newTestStorage(t)
	content := bytes.Repeat([]byte{0x24}, int(BlockSize+1))
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, int64(len(content)), readCloser(bytes.NewReader(content)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	var changed atomic.Bool
	storage.manifestHooks.afterReadBlock = func(index int) {
		if index != 0 || !changed.CompareAndSwap(false, true) {
			return
		}
		path := filepath.Join(storageTestSharePath(storage), stored.StorageName)
		changedMTime := stored.MTime.Add(time.Hour)
		if err := os.Chtimes(path, changedMTime, changedMTime); err != nil {
			t.Errorf("Chtimes manifest source: %v", err)
		}
	}
	if _, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "changing-mtime.bin"); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("mtime-changed file error = %v, want ErrFileChanged", err)
	}
}

func TestStorageAndManifestOutputPassTask10ScalarValidators(t *testing.T) {
	storage := newTestStorage(t)
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, 3, readCloser(strings.NewReader("abc")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	fileManifest, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "folder/report.txt")
	if err != nil {
		t.Fatalf("BuildFileManifest: %v", err)
	}
	blocks := fileManifest.Blocks()
	if len(blocks) != 1 || blocks[0].BLAKE3() != "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85" {
		t.Fatalf("abc block vector = %+v", blocks)
	}
	checks := []struct {
		name string
		err  error
	}{
		{name: "storage_name", err: sharefile.StorageNameValidator(stored.StorageName)},
		{name: "virtual_path", err: sharefile.VirtualPathValidator(fileManifest.VirtualPath())},
		{name: "size", err: sharefile.SizeBytesValidator(fileManifest.Size())},
		{name: "whole_hash", err: sharefile.Blake3Validator(fileManifest.BLAKE3())},
		{name: "block_size", err: sharefile.BlockSizeValidator(fileManifest.BlockSize())},
		{name: "block_hashes", err: sharefile.BlockHashesValidator(fileManifest.BlockHashes())},
	}
	for _, check := range checks {
		if check.err != nil {
			t.Errorf("Task 10 %s validator rejected Task 11 output: %v", check.name, check.err)
		}
	}
}

func TestStorageCloseWaitsForEnteredManifestAndCancelsIt(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "staging")
	storage, err := NewStorage(rootPath)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	content := bytes.Repeat([]byte{0x7a}, int(BlockSize+1))
	stored, err := storage.Store(t.Context(), testOwnerID, testShareID, int64(len(content)), readCloser(bytes.NewReader(content)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	storage.manifestHooks.afterReadBlock = func(index int) {
		if index == 0 {
			close(entered)
			<-release
		}
	}
	manifestDone := make(chan error, 1)
	go func() {
		_, err := storage.BuildFileManifest(t.Context(), testOwnerID, testShareID, stored.StorageName, testFileID, "close.bin")
		manifestDone <- err
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
		<-manifestDone
		t.Fatalf("Close returned before manifest exited: %v", err)
	default:
	}
	close(release)
	if err := <-manifestDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("manifest error = %v, want ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}
