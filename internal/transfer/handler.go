package transfer

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
)

var dummyCapabilityHash = sha256.Sum256([]byte("tailcat-transfer-capability-dummy-v1"))

// ReservedHandler binds the secure transfer protocol to one immutable Tailcat
// server ID. It closes every accepted connection before returning.
func (s *Service) ReservedHandler(serverID string) func(context.Context, net.Conn) {
	return func(ctx context.Context, connection net.Conn) {
		if connection == nil {
			return
		}
		defer connection.Close()
		if err := s.serveReserved(ctx, serverID, connection); err != nil {
			s.logger.DebugContext(ctx, "Tailcat transfer request ended", "server_id", serverID, "error_code", protocolCode(err))
		}
	}
}

func (s *Service) serveReserved(ctx context.Context, serverID string, connection net.Conn) error {
	requestCtx, cancel := context.WithTimeoutCause(ctx, protocolRequestTimeout, protocolError(CodeRemoteUnavailable, errors.New("transfer handler timed out")))
	defer cancel()
	stopRequest := context.AfterFunc(requestCtx, func() { _ = connection.Close() })
	defer stopRequest()
	if deadline, ok := requestCtx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return protocolError(CodeRemoteUnavailable, err)
		}
	}
	request, err := readRequestWithCapture(requestCtx, connection, s.captureSecret)
	if err != nil {
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	s.captureSecret("handler.request", request.Capability)
	defer request.clear()
	if validateEntityID(request.ShareID) != nil {
		err := protocolError(CodeInvalidCapability, ErrInvalidCapability)
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	admission, err := s.beginShareAdmission(requestCtx, request.ShareID)
	if err != nil {
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	reconcileAfterRequest := false
	reconcileOwnerID := ""
	reconcileCause := deletionRequested
	defer func() {
		s.finishShareAdmission(admission)
		if !reconcileAfterRequest || reconcileOwnerID == "" {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancelCleanup()
		if cleanupErr := s.deleteShare(cleanupCtx, reconcileOwnerID, request.ShareID, reconcileCause); cleanupErr != nil && !errors.Is(cleanupErr, ErrNotFound) {
			s.recordFailure(cleanupErr)
		}
	}()
	stopAdmission := context.AfterFunc(admission.ctx, func() { _ = connection.Close() })
	defer stopAdmission()
	share, err := s.authorizeRequest(admission.ctx, serverID, request)
	if err != nil {
		reconcileCause, reconcileAfterRequest = configuredDeletionCause(err)
		if reconcileAfterRequest && share != nil {
			reconcileOwnerID = share.UserID
		}
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	effectiveExpiry := s.effectiveExpiry(share.CreatedAt, share.ExpiresAt)
	if err := s.armShareExpiry(admission, effectiveExpiry); err != nil {
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	if s.handlerHooks.afterAuthorized != nil {
		s.handlerHooks.afterAuthorized()
	}
	streamCtx, err := s.commitShareAdmission(admission, effectiveExpiry)
	if err != nil {
		_ = writeErrorResponse(requestCtx, connection, responseCode(err))
		return err
	}
	stop := context.AfterFunc(streamCtx, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := streamCtx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return protocolError(CodeRemoteUnavailable, err)
		}
	}

	switch request.Operation {
	case operationManifest:
		err = s.serveManifest(streamCtx, connection, share)
	case operationRange:
		err = s.serveRange(streamCtx, connection, share, request)
	default:
		err = protocolError(CodeProtocolInvalid, errors.New("unsupported operation"))
	}
	if err != nil {
		_ = writeErrorResponse(streamCtx, connection, responseCode(err))
	}
	return err
}

func (s *Service) authorizeRequest(ctx context.Context, serverID string, request wireRequest) (*ent.TransferShare, error) {
	storedHash := dummyCapabilityHash[:]
	candidateHash := dummyCapabilityHash
	parsed, parseErr := parseCapabilityBytes(request.Capability)
	if parseErr == nil {
		s.captureSecret("authorization.secret", parsed.secret[:])
		defer parsed.clear()
	}
	if parseErr == nil {
		candidateHash = capabilitySecretHash(&parsed)
	}
	var row *ent.TransferShare
	var queryErr error
	now := time.Now().UTC()
	row, queryErr = s.db.TransferShare.Query().Where(
		transfershare.IDEQ(request.ShareID), transfershare.ServerIDEQ(serverID),
		transfershare.StatusEQ(transfershare.StatusReady),
	).Only(ctx)
	identityEligible := queryErr == nil && row != nil && parseErr == nil && parsed.shareID == request.ShareID && len(row.CapabilityHash) == sha256.Size
	if identityEligible {
		storedHash = row.CapabilityHash
	}
	matched := s.compareCapability(storedHash, candidateHash[:])
	if queryErr != nil && !ent.IsNotFound(queryErr) {
		return nil, protocolError(CodeRemoteUnavailable, errors.New("transfer metadata unavailable"))
	}
	if !identityEligible || matched != 1 {
		return nil, protocolError(CodeInvalidCapability, ErrInvalidCapability)
	}
	if err := s.validateShareLimits(ctx, row, now); err != nil {
		if cause, configured := configuredDeletionCause(err); configured {
			if cause == deletionExpired {
				return row, protocolError(CodeInvalidCapability, err)
			}
			return row, protocolError(CodeLimitExceeded, err)
		}
		return nil, protocolError(CodeRemoteUnavailable, errors.New("transfer metadata unavailable"))
	}
	return row, nil
}

func (s *Service) serveManifest(ctx context.Context, connection net.Conn, share *ent.TransferShare) error {
	rows, err := s.db.ShareFile.Query().Where(
		sharefile.UserIDEQ(share.UserID), sharefile.ShareIDEQ(share.ID),
	).Order(ent.Asc(sharefile.FieldCreatedAt), ent.Asc(sharefile.FieldID)).All(ctx)
	if err != nil {
		return protocolError(CodeRemoteUnavailable, errors.New("manifest unavailable"))
	}
	if len(rows) > MaxFilesPerShare {
		return protocolError(CodeLimitExceeded, errors.New("manifest file count exceeds limit"))
	}
	files := make([]manifestFileWire, len(rows))
	var totalBytes int64
	for index, row := range rows {
		if row.SizeBytes < 0 || row.SizeBytes > MaxFileBytes || totalBytes > MaxShareBytes-row.SizeBytes || len(row.BlockHashes) > 64 {
			return protocolError(CodeLimitExceeded, errors.New("manifest metadata exceeds limits"))
		}
		totalBytes += row.SizeBytes
		files[index] = manifestFileWire{
			FileID: row.ID, VirtualPath: row.VirtualPath, Size: row.SizeBytes,
			MTime: row.Mtime.UTC().Format(time.RFC3339Nano), BLAKE3: row.Blake3,
			BlockSize: row.BlockSize, BlockHashes: append([]string(nil), row.BlockHashes...),
		}
	}
	payload, err := json.Marshal(&manifestWire{Version: protocolVersion, ShareID: share.ID, BlockSize: BlockSize, Files: files})
	if err != nil {
		return protocolError(CodeProtocolInvalid, err)
	}
	if len(payload) > MaxManifestResponseBytes {
		return protocolError(CodeLimitExceeded, errors.New("manifest response exceeds limit"))
	}
	return writeSuccessResponse(ctx, connection, payload, MaxManifestResponseBytes)
}

func (s *Service) serveRange(ctx context.Context, connection net.Conn, share *ent.TransferShare, request wireRequest) error {
	if validateEntityID(request.FileID) != nil {
		return protocolError(CodeProtocolInvalid, errors.New("invalid file ID"))
	}
	row, err := s.db.ShareFile.Query().Where(
		sharefile.IDEQ(request.FileID), sharefile.UserIDEQ(share.UserID), sharefile.ShareIDEQ(share.ID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return protocolError(CodeShareNotFound, errors.New("file unavailable"))
	}
	if err != nil {
		return protocolError(CodeRemoteUnavailable, errors.New("file metadata unavailable"))
	}
	if err := validateManifestRange(row.SizeBytes, request.Offset, request.Length); err != nil {
		return err
	}
	handle, err := s.storage.Open(ctx, share.UserID, share.ID, row.StorageName)
	if err != nil {
		return protocolError(CodeStorageFailed, errors.New("staged file unavailable"))
	}
	defer handle.Close()
	data := make([]byte, int(request.Length))
	read, err := handle.ReadAt(data, request.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return protocolError(CodeStorageFailed, err)
	}
	if int64(read) != request.Length {
		return protocolError(CodeStorageFailed, io.ErrUnexpectedEOF)
	}
	if err := context.Cause(ctx); err != nil {
		return protocolError(CodeCanceled, err)
	}
	return writeSuccessResponse(ctx, connection, data, MaxRangeResponseBytes)
}

func validateManifestRange(fileSize, offset, length int64) error {
	if fileSize <= 0 || offset < 0 || length <= 0 || length > BlockSize || offset >= fileSize || offset%BlockSize != 0 || offset > fileSize-length {
		return protocolError(CodeProtocolInvalid, errors.New("invalid manifest range"))
	}
	want := min(BlockSize, fileSize-offset)
	if length != want {
		return protocolError(CodeProtocolInvalid, errors.New("range is not an exact manifest block"))
	}
	return nil
}

func responseCode(err error) ErrorCode {
	code := protocolCode(err)
	if !code.valid() {
		return CodeRemoteUnavailable
	}
	return code
}
