package transfer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zeebo/blake3"
)

const BlockSize int64 = 8 * 1024 * 1024

var ErrFileChanged = errors.New("transfer file changed while hashing")

// Block is one immutable, ordered BLAKE3-verified file range.
type Block struct {
	index  int
	offset int64
	size   int64
	blake3 string
}

func (b Block) Index() int     { return b.index }
func (b Block) Offset() int64  { return b.offset }
func (b Block) Size() int64    { return b.size }
func (b Block) BLAKE3() string { return b.blake3 }

// FileManifest is immutable transfer identity and integrity metadata for one
// staged regular file.
type FileManifest struct {
	fileID      string
	virtualPath string
	size        int64
	mtime       time.Time
	blake3      string
	blocks      []Block
}

func (m FileManifest) FileID() string      { return m.fileID }
func (m FileManifest) VirtualPath() string { return m.virtualPath }
func (m FileManifest) Size() int64         { return m.size }
func (m FileManifest) MTime() time.Time    { return m.mtime }
func (m FileManifest) BLAKE3() string      { return m.blake3 }
func (m FileManifest) BlockSize() int64    { return BlockSize }
func (m FileManifest) Blocks() []Block     { return slices.Clone(m.blocks) }
func (m FileManifest) BlockHashes() []string {
	hashes := make([]string, len(m.blocks))
	for index, block := range m.blocks {
		hashes[index] = block.blake3
	}
	return hashes
}

// Manifest is an immutable ordered collection of file manifests.
type Manifest struct {
	files []FileManifest
}

func NewManifest(files ...FileManifest) Manifest {
	cloned := make([]FileManifest, len(files))
	for index, file := range files {
		cloned[index] = cloneFileManifest(file)
	}
	return Manifest{files: cloned}
}

func (m Manifest) Files() []FileManifest {
	files := make([]FileManifest, len(m.files))
	for index, file := range m.files {
		files[index] = cloneFileManifest(file)
	}
	return files
}

type manifestHooks struct {
	workerStarted  func()
	workerStopped  func()
	afterReadBlock func(int)
}

type blockJob struct {
	index  int
	offset int64
	data   []byte
	buffer *[]byte
}

type blockResult struct {
	index int
	block Block
}

// BuildFileManifest hashes one validated Storage-owned file without accepting
// or returning a host filesystem path.
func (s *Storage) BuildFileManifest(ctx context.Context, ownerID, shareID, storageName, fileID, virtualPath string) (_ FileManifest, retErr error) {
	operationCtx, end, err := s.beginOperation(ctx)
	if err != nil {
		return FileManifest{}, err
	}
	defer end()
	return s.buildFileManifest(operationCtx, ownerID, shareID, storageName, fileID, virtualPath)
}

func (s *Storage) buildFileManifest(ctx context.Context, ownerID, shareID, storageName, fileID, virtualPath string) (_ FileManifest, retErr error) {
	if err := contextError(ctx); err != nil {
		return FileManifest{}, err
	}
	if err := validateEntityID(fileID); err != nil {
		return FileManifest{}, fmt.Errorf("file ID: %w", err)
	}
	if err := validateVirtualPath(virtualPath); err != nil {
		return FileManifest{}, err
	}
	file, err := s.open(ctx, ownerID, shareID, storageName)
	if err != nil {
		return FileManifest{}, err
	}
	defer func() {
		if closeErr := file.Close(); retErr == nil && closeErr != nil {
			retErr = fmt.Errorf("close manifest source: %w", closeErr)
		}
	}()

	fileManifest, sourceInfo, err := buildFileManifest(ctx, file, fileID, virtualPath, s.manifestHooks)
	if err != nil {
		return FileManifest{}, err
	}
	if err := s.operationError(ctx); err != nil {
		return FileManifest{}, err
	}
	shareRoot, err := s.openShare(ownerID, shareID, false)
	if err != nil {
		return FileManifest{}, err
	}
	current, currentErr := safeRegularInfo(shareRoot, storageName)
	closeErr := shareRoot.Close()
	if currentErr != nil {
		return FileManifest{}, currentErr
	}
	if closeErr != nil {
		return FileManifest{}, fmt.Errorf("close share root after manifest: %w", closeErr)
	}
	if !os.SameFile(sourceInfo, current) || current.Size() != fileManifest.size || !current.ModTime().Equal(fileManifest.mtime) {
		return FileManifest{}, ErrFileChanged
	}
	if err := s.operationError(ctx); err != nil {
		return FileManifest{}, err
	}
	return fileManifest, nil
}

func buildFileManifest(ctx context.Context, file *os.File, fileID, virtualPath string, hooks manifestHooks) (FileManifest, os.FileInfo, error) {
	startInfo, err := file.Stat()
	if err != nil {
		return FileManifest{}, nil, fmt.Errorf("stat manifest source: %w", err)
	}
	if !startInfo.Mode().IsRegular() {
		return FileManifest{}, nil, ErrNotRegular
	}
	if startInfo.Size() < 0 || startInfo.Size() > MaxFileBytes {
		return FileManifest{}, nil, ErrFileTooLarge
	}

	blockCount := manifestBlockCount(startInfo.Size())
	blocks, wholeHash, err := hashManifestBlocks(ctx, file, startInfo.Size(), blockCount, hooks)
	if err != nil {
		return FileManifest{}, nil, err
	}
	endInfo, err := file.Stat()
	if err != nil {
		return FileManifest{}, nil, fmt.Errorf("restat manifest source: %w", err)
	}
	if !endInfo.Mode().IsRegular() || !os.SameFile(startInfo, endInfo) || startInfo.Size() != endInfo.Size() || !startInfo.ModTime().Equal(endInfo.ModTime()) {
		return FileManifest{}, nil, ErrFileChanged
	}
	return FileManifest{
		fileID:      fileID,
		virtualPath: virtualPath,
		size:        startInfo.Size(),
		mtime:       startInfo.ModTime().UTC(),
		blake3:      wholeHash,
		blocks:      blocks,
	}, endInfo, nil
}

func hashManifestBlocks(ctx context.Context, file *os.File, size int64, blockCount int, hooks manifestHooks) ([]Block, string, error) {
	workerContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan blockJob)
	results := make(chan blockResult, blockCount)
	bufferPool := sync.Pool{New: func() any {
		buffer := make([]byte, int(BlockSize))
		return &buffer
	}}

	var workers sync.WaitGroup
	for range manifestWorkerCount() {
		workers.Go(func() {
			if hooks.workerStarted != nil {
				hooks.workerStarted()
			}
			if hooks.workerStopped != nil {
				defer hooks.workerStopped()
			}
			for {
				select {
				case <-workerContext.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					hash := blake3.Sum256(job.data)
					block := Block{index: job.index, offset: job.offset, size: int64(len(job.data)), blake3: hex.EncodeToString(hash[:])}
					bufferPool.Put(job.buffer)
					select {
					case results <- blockResult{index: job.index, block: block}:
					case <-workerContext.Done():
						return
					}
				}
			}
		})
	}

	whole := blake3.New()
	var produceErr error
	for index := range blockCount {
		if err := workerContext.Err(); err != nil {
			produceErr = context.Cause(workerContext)
			break
		}
		offset := int64(index) * BlockSize
		length := min(BlockSize, size-offset)
		buffer := bufferPool.Get().(*[]byte)
		data := (*buffer)[:int(length)]
		read, err := file.ReadAt(data, offset)
		if err != nil || read != len(data) {
			bufferPool.Put(buffer)
			produceErr = errors.Join(err, io.ErrUnexpectedEOF)
			cancel(produceErr)
			break
		}
		_, _ = whole.Write(data)
		select {
		case jobs <- blockJob{index: index, offset: offset, data: data, buffer: buffer}:
		case <-workerContext.Done():
			bufferPool.Put(buffer)
			produceErr = context.Cause(workerContext)
		}
		if produceErr != nil {
			break
		}
		if hooks.afterReadBlock != nil {
			hooks.afterReadBlock(index)
		}
	}
	close(jobs)
	workers.Wait()
	if produceErr != nil {
		return nil, "", produceErr
	}
	if err := workerContext.Err(); err != nil {
		return nil, "", context.Cause(workerContext)
	}
	if len(results) != blockCount {
		return nil, "", io.ErrUnexpectedEOF
	}
	blocks := make([]Block, blockCount)
	for range blockCount {
		result := <-results
		blocks[result.index] = result.block
	}
	return blocks, hex.EncodeToString(whole.Sum(nil)), nil
}

func manifestWorkerCount() int {
	return max(1, min(runtime.GOMAXPROCS(0), 4))
}

func manifestBlockCount(size int64) int {
	if size == 0 {
		return 0
	}
	return int((size + BlockSize - 1) / BlockSize)
}

func validateVirtualPath(virtualPath string) error {
	if virtualPath == "" || len(virtualPath) > maxBoundaryBytes || !utf8.ValidString(virtualPath) || strings.ContainsRune(virtualPath, 0) || strings.HasPrefix(virtualPath, "/") || strings.ContainsAny(virtualPath, "\\:") {
		return fmt.Errorf("%w: virtual path must be canonical and relative", ErrInvalidPath)
	}
	segments := strings.Split(virtualPath, "/")
	if len(segments) > maxBoundaryDepth {
		return fmt.Errorf("%w: virtual path depth", ErrInvalidPath)
	}
	for _, segment := range segments {
		canonical := strings.TrimRight(segment, " .")
		if canonical != segment || canonical == "" || canonical == "." || canonical == ".." || isWindowsDeviceName(canonical) {
			return fmt.Errorf("%w: virtual path segment", ErrInvalidPath)
		}
	}
	return nil
}

func cloneFileManifest(file FileManifest) FileManifest {
	file.blocks = slices.Clone(file.blocks)
	return file
}
