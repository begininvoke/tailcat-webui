package diagnostics

import (
	"bytes"
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"time"
)

const (
	protocolVersion     uint8 = 1
	responseStatusOK          = "ok"
	responseStatusError       = "error"
)

var wireMagic = []byte("TCATDG01")

type requestHeader struct {
	Version    uint8  `json:"version"`
	Operation  string `json:"operation"`
	DurationMS int64  `json:"duration_ms"`
	Bytes      int64  `json:"bytes,omitzero"`
}

type responseHeader struct {
	Version uint8     `json:"v"`
	Status  string    `json:"s"`
	Code    ErrorCode `json:"c,omitzero"`
}

// Handler serves the fixed, resource-bounded peer diagnostic protocol. Its
// zero value is ready to use and does not depend on Tailcat runtime types.
type Handler struct{}

// Serve handles one reserved-port connection. Callers should close conn after
// it returns; Handler clears neither ownership nor peer identity into storage.
func (Handler) Serve(ctx context.Context, conn net.Conn) error {
	started := time.Now()
	maxCtx, cancelMax := context.WithTimeout(ctx, MaxDuration)
	defer cancelMax()
	stopMax := interruptOnCancel(maxCtx, conn)
	defer stopMax()
	if err := setDeadline(maxCtx, conn); err != nil {
		return protocolError(CodeIO, err)
	}

	header, inputBytes, err := readRequestHeader(maxCtx, conn)
	if err != nil {
		writeProtocolFailure(maxCtx, conn, inputBytes, err)
		return err
	}
	request, err := header.request()
	if err != nil {
		writeProtocolFailure(maxCtx, conn, inputBytes, err)
		return err
	}

	requestDeadline := started.Add(request.Duration)
	requestCtx, cancelRequest := context.WithDeadline(maxCtx, requestDeadline)
	defer cancelRequest()
	stopRequest := interruptOnCancel(requestCtx, conn)
	defer stopRequest()
	if err := setDeadline(requestCtx, conn); err != nil {
		return protocolError(CodeIO, err)
	}

	if err := writeResponseIfSafe(requestCtx, conn, inputBytes, responseHeader{Version: protocolVersion, Status: responseStatusOK}); err != nil {
		return err
	}
	if request.Kind == RunKindPing {
		return nil
	}
	if err := discardExact(requestCtx, conn, request.Bytes); err != nil {
		return err
	}
	return writeZeros(requestCtx, conn, request.Bytes)
}

// Run dials a preselected peer and executes one protocol request. Duration and
// byte ceilings are checked before dialing, so invalid input consumes no peer
// connection or protocol bandwidth.
func (r Runner) Run(ctx context.Context, request Request) (Result, error) {
	header, err := requestHeaderFor(request)
	if err != nil {
		return Result{}, err
	}
	if r.dial == nil {
		return Result{}, protocolError(CodeInvalidRunner, errors.New("nil diagnostic dialer"))
	}

	operationCtx, cancel := context.WithTimeout(ctx, time.Duration(header.DurationMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := r.dial(operationCtx)
	if err != nil {
		return Result{}, classifyIOError(operationCtx, err)
	}
	defer conn.Close()
	stop := interruptOnCancel(operationCtx, conn)
	defer stop()
	if err := setDeadline(operationCtx, conn); err != nil {
		return Result{}, protocolError(CodeIO, err)
	}
	if err := writeRequestHeader(operationCtx, conn, header); err != nil {
		return Result{}, err
	}
	response, err := readResponse(operationCtx, conn)
	if err != nil {
		return Result{}, err
	}
	if response.Status == responseStatusError {
		return Result{}, protocolError(response.Code, errors.New("remote diagnostic failure"))
	}

	result := Result{Kind: request.Kind}
	if request.Kind == RunKindPing {
		result.Latency = time.Since(started)
		result.Duration = result.Latency
		return result, nil
	}
	if err := writeZeros(operationCtx, conn, request.Bytes); err != nil {
		return Result{}, err
	}
	result.UploadBytes = request.Bytes
	if err := discardExact(operationCtx, conn, request.Bytes); err != nil {
		return Result{}, err
	}
	result.DownloadBytes = request.Bytes
	result.Duration = time.Since(started)
	return result, nil
}

func readRequestHeader(ctx context.Context, conn net.Conn) (requestHeader, int, error) {
	magic := make([]byte, len(wireMagic))
	if err := readFull(ctx, conn, magic); err != nil {
		return requestHeader{}, 0, err
	}
	if !bytes.Equal(magic, wireMagic) {
		return requestHeader{}, 0, protocolError(CodeInvalidMagic, errors.New("unexpected protocol magic"))
	}
	var frame [2]byte
	if err := readFull(ctx, conn, frame[:]); err != nil {
		return requestHeader{}, 0, err
	}
	length := binary.BigEndian.Uint16(frame[:])
	if length > MaxHeaderBytes {
		return requestHeader{}, 0, protocolError(CodeHeaderTooLarge, errors.New("header exceeds limit"))
	}
	body := make([]byte, length)
	limited := &io.LimitedReader{R: conn, N: int64(length)}
	if err := readFull(ctx, limited, body); err != nil {
		return requestHeader{}, 0, err
	}
	inputBytes := len(wireMagic) + len(frame) + len(body)
	var header requestHeader
	if err := json.Unmarshal(body, &header, json.RejectUnknownMembers(true)); err != nil {
		return requestHeader{}, inputBytes, protocolError(CodeMalformedHeader, err)
	}
	return header, inputBytes, nil
}

func writeRequestHeader(ctx context.Context, conn net.Conn, header requestHeader) error {
	body, err := json.Marshal(&header)
	if err != nil {
		return protocolError(CodeInvalidRequest, err)
	}
	if len(body) > MaxHeaderBytes {
		return protocolError(CodeHeaderTooLarge, errors.New("encoded header exceeds limit"))
	}
	frame := make([]byte, len(wireMagic)+2+len(body))
	copy(frame, wireMagic)
	binary.BigEndian.PutUint16(frame[len(wireMagic):], uint16(len(body)))
	copy(frame[len(wireMagic)+2:], body)
	return writeAll(ctx, conn, frame)
}

func readResponse(ctx context.Context, conn net.Conn) (responseHeader, error) {
	var frame [2]byte
	if err := readFull(ctx, conn, frame[:]); err != nil {
		return responseHeader{}, err
	}
	length := binary.BigEndian.Uint16(frame[:])
	if length > MaxHeaderBytes {
		return responseHeader{}, protocolError(CodeHeaderTooLarge, errors.New("response exceeds limit"))
	}
	body := make([]byte, length)
	limited := &io.LimitedReader{R: conn, N: int64(length)}
	if err := readFull(ctx, limited, body); err != nil {
		return responseHeader{}, err
	}
	var response responseHeader
	if err := json.Unmarshal(body, &response, json.RejectUnknownMembers(true)); err != nil {
		return responseHeader{}, protocolError(CodeMalformedHeader, err)
	}
	if response.Version != protocolVersion {
		return responseHeader{}, protocolError(CodeMalformedHeader, errors.New("unsupported response version"))
	}
	switch response.Status {
	case responseStatusOK:
		if response.Code != "" {
			return responseHeader{}, protocolError(CodeMalformedHeader, errors.New("success response includes error code"))
		}
	case responseStatusError:
		if !response.Code.valid() {
			return responseHeader{}, protocolError(CodeMalformedHeader, errors.New("invalid response error code"))
		}
	default:
		return responseHeader{}, protocolError(CodeMalformedHeader, errors.New("invalid response status"))
	}
	return response, nil
}

func writeProtocolFailure(ctx context.Context, conn net.Conn, inputBytes int, err error) {
	protocolErr, ok := errors.AsType[*ProtocolError](err)
	if !ok || !protocolErr.Code.valid() {
		return
	}
	_ = writeResponseIfSafe(ctx, conn, inputBytes, responseHeader{
		Version: protocolVersion,
		Status:  responseStatusError,
		Code:    protocolErr.Code,
	})
}

func writeResponseIfSafe(ctx context.Context, conn net.Conn, inputBytes int, response responseHeader) error {
	frame, err := responseFrame(response)
	if err != nil {
		return err
	}
	if len(frame) > inputBytes {
		return nil
	}
	return writeAll(ctx, conn, frame)
}

func responseFrame(response responseHeader) ([]byte, error) {
	body, err := json.Marshal(&response)
	if err != nil {
		return nil, protocolError(CodeMalformedHeader, err)
	}
	if len(body) > MaxHeaderBytes {
		return nil, protocolError(CodeHeaderTooLarge, errors.New("encoded response exceeds limit"))
	}
	frame := make([]byte, len(body)+2)
	binary.BigEndian.PutUint16(frame, uint16(len(body)))
	copy(frame[2:], body)
	return frame, nil
}

func discardExact(ctx context.Context, conn net.Conn, size int64) error {
	limited := &io.LimitedReader{R: conn, N: size}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return classifyIOError(ctx, err)
	}
	if limited.N != 0 {
		return protocolError(CodeIO, io.ErrUnexpectedEOF)
	}
	return nil
}

func writeZeros(ctx context.Context, conn net.Conn, size int64) error {
	var block [32 << 10]byte
	for size > 0 {
		chunk := min(size, int64(len(block)))
		if err := writeAll(ctx, conn, block[:chunk]); err != nil {
			return err
		}
		size -= chunk
	}
	return nil
}

func readFull(ctx context.Context, reader io.Reader, data []byte) error {
	if _, err := io.ReadFull(reader, data); err != nil {
		return classifyIOError(ctx, err)
	}
	return nil
}

func writeAll(ctx context.Context, conn net.Conn, data []byte) error {
	for len(data) > 0 {
		written, err := conn.Write(data)
		if err != nil {
			return classifyIOError(ctx, err)
		}
		if written == 0 {
			return protocolError(CodeIO, io.ErrShortWrite)
		}
		data = data[written:]
	}
	return nil
}

func setDeadline(ctx context.Context, conn net.Conn) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return conn.SetDeadline(deadline)
}

func interruptOnCancel(ctx context.Context, conn net.Conn) func() bool {
	return context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
}

func classifyIOError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return protocolError(CodeCanceled, cause)
		}
		return protocolError(CodeTimeout, cause)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return protocolError(CodeTimeout, err)
	}
	return protocolError(CodeIO, err)
}
