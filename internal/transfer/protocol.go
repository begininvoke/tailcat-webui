package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"uuid"
)

const (
	// ReservedPort is the fixed Tailcat TCP port for secure staged transfers.
	ReservedPort uint16 = 41641

	protocolVersion = 2

	MaxRequestFrameBytes = 8 << 10
	// Eight MiB covers 1,000 files with the maximum 64 lowercase 32-byte
	// block hashes each (about 4.3 MiB) plus all fixed manifest metadata.
	MaxManifestResponseBytes = 8 << 20
	// A range response is exactly one manifest block, including the shorter
	// final block, and can therefore never exceed eight MiB.
	MaxRangeResponseBytes  = int(BlockSize)
	maxErrorResponseBytes  = 512
	protocolRequestTimeout = 30 * time.Second

	capabilityPrefix       = "tcs1."
	capabilitySecretBytes  = 32
	capabilityPayloadBytes = 16 + capabilitySecretBytes

	responseStatusSuccess byte = 0
	responseStatusError   byte = 1
)

const (
	operationManifest = "manifest"
	operationRange    = "range"
)

type ErrorCode string

const (
	CodeCanceled          ErrorCode = "transfer_canceled"
	CodeExpired           ErrorCode = "transfer_expired"
	CodeRemoteUnavailable ErrorCode = "transfer_remote_unavailable"
	CodeInvalidCapability ErrorCode = "transfer_invalid_capability"
	CodeShareNotFound     ErrorCode = "transfer_share_not_found"
	CodeProtocolInvalid   ErrorCode = "transfer_protocol_invalid"
	CodeIntegrityMismatch ErrorCode = "transfer_integrity_mismatch"
	CodeStorageFailed     ErrorCode = "transfer_storage_failed"
	CodeLimitExceeded     ErrorCode = "transfer_limit_exceeded"
)

var ErrInvalidCapability = errors.New("invalid transfer capability")

// ProtocolError exposes only a stable machine code. The wrapped cause remains
// available locally and is never serialized onto the wire.
type ProtocolError struct {
	Code  ErrorCode
	cause error
}

func (e *ProtocolError) Error() string { return string(e.Code) }
func (e *ProtocolError) Unwrap() error { return e.cause }

func protocolError(code ErrorCode, cause error) error {
	return &ProtocolError{Code: code, cause: cause}
}

func protocolCode(err error) ErrorCode {
	if protocolErr, ok := errors.AsType[*ProtocolError](err); ok {
		return protocolErr.Code
	}
	return ""
}

func (code ErrorCode) valid() bool {
	switch code {
	case CodeCanceled, CodeExpired, CodeRemoteUnavailable, CodeInvalidCapability,
		CodeShareNotFound, CodeProtocolInvalid, CodeIntegrityMismatch,
		CodeStorageFailed, CodeLimitExceeded:
		return true
	default:
		return false
	}
}

type parsedCapability struct {
	shareID string
	secret  [capabilitySecretBytes]byte
}

func encodeCapability(shareID string, secret [capabilitySecretBytes]byte) (string, []byte, error) {
	if err := validateEntityID(shareID); err != nil {
		return "", nil, fmt.Errorf("%w: share ID", ErrInvalidCapability)
	}
	parsed, err := uuid.Parse(shareID)
	if err != nil {
		return "", nil, fmt.Errorf("%w: share ID", ErrInvalidCapability)
	}
	payload := make([]byte, capabilityPayloadBytes)
	copy(payload, parsed[:])
	copy(payload[16:], secret[:])
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	hash := sha256.Sum256(secret[:])
	return capabilityPrefix + encoded, hash[:], nil
}

func parseCapability(code string) (parsedCapability, error) {
	payloadText, ok := strings.CutPrefix(code, capabilityPrefix)
	if !ok || len(payloadText) != base64.RawURLEncoding.EncodedLen(capabilityPayloadBytes) {
		return parsedCapability{}, ErrInvalidCapability
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadText)
	if err != nil || len(payload) != capabilityPayloadBytes || base64.RawURLEncoding.EncodeToString(payload) != payloadText {
		return parsedCapability{}, ErrInvalidCapability
	}
	var shareUUID uuid.UUID
	copy(shareUUID[:], payload[:16])
	shareID := shareUUID.String()
	if shareUUID[6]>>4 != 7 || shareUUID[8]&0xc0 != 0x80 || validateEntityID(shareID) != nil {
		return parsedCapability{}, ErrInvalidCapability
	}
	parsed := parsedCapability{shareID: shareID}
	copy(parsed.secret[:], payload[16:])
	return parsed, nil
}

func capabilitySecretHash(parsed parsedCapability) [sha256.Size]byte {
	return sha256.Sum256(parsed.secret[:])
}

type wireRequest struct {
	Version    int    `json:"version"`
	ShareID    string `json:"share_id"`
	Capability string `json:"capability"`
	Operation  string `json:"operation"`
	FileID     string `json:"file_id"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
}

func (request wireRequest) validate() error {
	if request.Version != protocolVersion || request.ShareID == "" || request.Capability == "" {
		return protocolError(CodeProtocolInvalid, errors.New("invalid request envelope"))
	}
	switch request.Operation {
	case operationManifest:
		if request.FileID != "" || request.Offset != 0 || request.Length != 0 {
			return protocolError(CodeProtocolInvalid, errors.New("manifest request includes range fields"))
		}
	case operationRange:
		if request.FileID == "" || request.Offset < 0 || request.Length <= 0 || request.Length > BlockSize || request.Offset > int64(^uint64(0)>>1)-request.Length {
			return protocolError(CodeProtocolInvalid, errors.New("invalid range request"))
		}
	default:
		return protocolError(CodeProtocolInvalid, errors.New("unsupported transfer operation"))
	}
	return nil
}

func decodeRequestFrame(body []byte) (wireRequest, error) {
	if len(body) == 0 {
		return wireRequest{}, protocolError(CodeProtocolInvalid, errors.New("empty request frame"))
	}
	if len(body) > MaxRequestFrameBytes {
		return wireRequest{}, protocolError(CodeLimitExceeded, errors.New("request frame exceeds limit"))
	}
	var request wireRequest
	if err := json.Unmarshal(body, &request, json.RejectUnknownMembers(true)); err != nil {
		return wireRequest{}, protocolError(CodeProtocolInvalid, err)
	}
	if err := request.validate(); err != nil {
		return wireRequest{}, err
	}
	return request, nil
}

func readRequest(ctx context.Context, conn net.Conn) (wireRequest, error) {
	length, err := readFrameLength(ctx, conn)
	if err != nil {
		return wireRequest{}, err
	}
	if length == 0 {
		return wireRequest{}, protocolError(CodeProtocolInvalid, errors.New("empty request frame"))
	}
	if length > MaxRequestFrameBytes {
		return wireRequest{}, protocolError(CodeLimitExceeded, errors.New("request frame exceeds limit"))
	}
	body := make([]byte, int(length))
	if err := readFull(ctx, conn, body); err != nil {
		return wireRequest{}, err
	}
	return decodeRequestFrame(body)
}

func writeRequest(ctx context.Context, conn net.Conn, request wireRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	body, err := json.Marshal(&request)
	if err != nil {
		return protocolError(CodeProtocolInvalid, err)
	}
	if len(body) > MaxRequestFrameBytes {
		return protocolError(CodeLimitExceeded, errors.New("encoded request frame exceeds limit"))
	}
	return writeFrame(ctx, conn, body)
}

type protocolErrorWire struct {
	Version int       `json:"version"`
	Code    ErrorCode `json:"code"`
}

func writeSuccessResponse(ctx context.Context, conn net.Conn, payload []byte, maxPayload int) error {
	if len(payload) > maxPayload {
		return protocolError(CodeLimitExceeded, errors.New("success response exceeds limit"))
	}
	return writeResponseFrame(ctx, conn, responseStatusSuccess, payload)
}

func writeErrorResponse(ctx context.Context, conn net.Conn, code ErrorCode) error {
	if !code.valid() {
		code = CodeProtocolInvalid
	}
	payload, err := json.Marshal(&protocolErrorWire{Version: protocolVersion, Code: code})
	if err != nil {
		return protocolError(CodeProtocolInvalid, err)
	}
	if len(payload) > maxErrorResponseBytes {
		return protocolError(CodeLimitExceeded, errors.New("error response exceeds limit"))
	}
	return writeResponseFrame(ctx, conn, responseStatusError, payload)
}

func writeResponseFrame(ctx context.Context, conn net.Conn, status byte, payload []byte) error {
	length := len(payload) + 1
	if uint64(length) > uint64(^uint32(0)) {
		return protocolError(CodeLimitExceeded, errors.New("response frame exceeds uint32"))
	}
	frame := make([]byte, 4+length)
	binary.BigEndian.PutUint32(frame[:4], uint32(length))
	frame[4] = status
	copy(frame[5:], payload)
	return writeAll(ctx, conn, frame)
}

func readResponse(ctx context.Context, conn net.Conn, maxSuccessPayload int) ([]byte, error) {
	length, err := readFrameLength(ctx, conn)
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, protocolError(CodeProtocolInvalid, errors.New("empty response frame"))
	}
	maxFrame := maxSuccessPayload + 1
	if uint64(length) > uint64(maxFrame) {
		return nil, protocolError(CodeLimitExceeded, errors.New("response frame exceeds limit"))
	}
	frame := make([]byte, int(length))
	if err := readFull(ctx, conn, frame); err != nil {
		return nil, err
	}
	switch frame[0] {
	case responseStatusSuccess:
		return frame[1:], nil
	case responseStatusError:
		if len(frame)-1 > maxErrorResponseBytes {
			return nil, protocolError(CodeLimitExceeded, errors.New("error response exceeds limit"))
		}
		var response protocolErrorWire
		if err := json.Unmarshal(frame[1:], &response, json.RejectUnknownMembers(true)); err != nil || response.Version != protocolVersion || !response.Code.valid() {
			return nil, protocolError(CodeProtocolInvalid, errors.Join(err, errors.New("invalid error response")))
		}
		return nil, protocolError(response.Code, errors.New("remote transfer failure"))
	default:
		return nil, protocolError(CodeProtocolInvalid, errors.New("unknown response status"))
	}
}

func readFrameLength(ctx context.Context, reader io.Reader) (uint32, error) {
	var prefix [4]byte
	if err := readFull(ctx, reader, prefix[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(prefix[:]), nil
}

func writeFrame(ctx context.Context, writer io.Writer, payload []byte) error {
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return protocolError(CodeLimitExceeded, errors.New("frame exceeds uint32"))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(payload)))
	if err := writeAll(ctx, writer, prefix[:]); err != nil {
		return err
	}
	return writeAll(ctx, writer, payload)
}

func readFull(ctx context.Context, reader io.Reader, data []byte) error {
	if _, err := io.ReadFull(&io.LimitedReader{R: reader, N: int64(len(data))}, data); err != nil {
		return classifyProtocolIO(ctx, err)
	}
	return nil
}

func writeAll(ctx context.Context, writer io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := context.Cause(ctx); err != nil {
			return classifyProtocolIO(ctx, err)
		}
		written, err := writer.Write(data)
		if err != nil {
			return classifyProtocolIO(ctx, err)
		}
		if written <= 0 {
			return protocolError(CodeRemoteUnavailable, io.ErrShortWrite)
		}
		data = data[written:]
	}
	return nil
}

func classifyProtocolIO(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return protocolError(CodeCanceled, cause)
		}
		return protocolError(CodeRemoteUnavailable, cause)
	}
	return protocolError(CodeProtocolInvalid, err)
}

type manifestWire struct {
	Version   int                `json:"version"`
	ShareID   string             `json:"share_id"`
	BlockSize int64              `json:"block_size"`
	Files     []manifestFileWire `json:"files"`
}

type manifestFileWire struct {
	FileID      string   `json:"file_id"`
	VirtualPath string   `json:"virtual_path"`
	Size        int64    `json:"size"`
	MTime       string   `json:"mtime"`
	BLAKE3      string   `json:"blake3"`
	BlockSize   int64    `json:"block_size"`
	BlockHashes []string `json:"block_hashes"`
}

func validateManifestWire(wire manifestWire, expectedShareID string) (Manifest, error) {
	if wire.Version != protocolVersion || wire.ShareID != expectedShareID || wire.BlockSize != BlockSize || validateEntityID(wire.ShareID) != nil {
		return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("invalid manifest envelope"))
	}
	if len(wire.Files) > MaxFilesPerShare {
		return Manifest{}, protocolError(CodeLimitExceeded, errors.New("manifest file count exceeds limit"))
	}
	fileIDs := make(map[string]struct{}, len(wire.Files))
	paths := make(map[string]struct{}, len(wire.Files))
	files := make([]FileManifest, 0, len(wire.Files))
	var total int64
	for _, file := range wire.Files {
		if validateEntityID(file.FileID) != nil || validateVirtualPath(file.VirtualPath) != nil || file.Size < 0 || file.Size > MaxFileBytes || file.BlockSize != BlockSize || !validBLAKE3(file.BLAKE3) {
			return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("invalid manifest file"))
		}
		if _, exists := fileIDs[file.FileID]; exists {
			return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("duplicate manifest file ID"))
		}
		if _, exists := paths[file.VirtualPath]; exists {
			return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("duplicate manifest virtual path"))
		}
		fileIDs[file.FileID] = struct{}{}
		paths[file.VirtualPath] = struct{}{}
		mtime, err := time.Parse(time.RFC3339Nano, file.MTime)
		if err != nil || !strings.HasSuffix(file.MTime, "Z") || mtime.UTC().Format(time.RFC3339Nano) != file.MTime {
			return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("manifest mtime is not canonical UTC"))
		}
		blockCount := manifestBlockCount(file.Size)
		if len(file.BlockHashes) != blockCount {
			return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("manifest block count mismatch"))
		}
		blocks := make([]Block, blockCount)
		for index, hash := range file.BlockHashes {
			if !validBLAKE3(hash) {
				return Manifest{}, protocolError(CodeProtocolInvalid, errors.New("invalid manifest block hash"))
			}
			offset := int64(index) * BlockSize
			size := min(BlockSize, file.Size-offset)
			blocks[index] = Block{index: index, offset: offset, size: size, blake3: hash}
		}
		if total > MaxShareBytes-file.Size {
			return Manifest{}, protocolError(CodeLimitExceeded, errors.New("manifest byte total exceeds limit"))
		}
		total += file.Size
		files = append(files, FileManifest{
			fileID: file.FileID, virtualPath: file.VirtualPath, size: file.Size,
			mtime: mtime.UTC(), blake3: file.BLAKE3, blocks: blocks,
		})
	}
	return NewManifest(files...), nil
}

func validBLAKE3(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type protocolDial func(context.Context) (net.Conn, error)

func fetchManifest(ctx context.Context, dial protocolDial, shareID, capability string) (Manifest, error) {
	payload, err := executeProtocolRequest(ctx, dial, wireRequest{
		Version: protocolVersion, ShareID: shareID, Capability: capability, Operation: operationManifest,
	}, MaxManifestResponseBytes)
	if err != nil {
		return Manifest{}, err
	}
	var response manifestWire
	if err := json.Unmarshal(payload, &response, json.RejectUnknownMembers(true)); err != nil {
		return Manifest{}, protocolError(CodeProtocolInvalid, err)
	}
	return validateManifestWire(response, shareID)
}

func fetchRange(ctx context.Context, dial protocolDial, shareID, capability, fileID string, offset, length int64) ([]byte, error) {
	payload, err := executeProtocolRequest(ctx, dial, wireRequest{
		Version: protocolVersion, ShareID: shareID, Capability: capability,
		Operation: operationRange, FileID: fileID, Offset: offset, Length: length,
	}, MaxRangeResponseBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != length {
		return nil, protocolError(CodeProtocolInvalid, io.ErrUnexpectedEOF)
	}
	return payload, nil
}

func executeProtocolRequest(ctx context.Context, dial protocolDial, request wireRequest, maxResponse int) ([]byte, error) {
	if dial == nil {
		return nil, protocolError(CodeRemoteUnavailable, errors.New("nil transfer dialer"))
	}
	requestCtx, cancel := context.WithTimeoutCause(ctx, protocolRequestTimeout, protocolError(CodeRemoteUnavailable, errors.New("transfer request timed out")))
	defer cancel()
	conn, err := dial(requestCtx)
	if err != nil {
		return nil, protocolError(CodeRemoteUnavailable, err)
	}
	if conn == nil {
		return nil, protocolError(CodeRemoteUnavailable, errors.New("transfer dialer returned nil connection"))
	}
	defer conn.Close()
	stop := context.AfterFunc(requestCtx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := requestCtx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, protocolError(CodeRemoteUnavailable, err)
		}
	}
	if err := writeRequest(requestCtx, conn, request); err != nil {
		return nil, err
	}
	return readResponse(requestCtx, conn, maxResponse)
}
