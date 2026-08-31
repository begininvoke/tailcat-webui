package diagnostics

import (
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"time"
)

const (
	protocolVersion uint8 = 1
	ackByte               = byte(1)
)

var wireMagic = []byte("TCATDG01")

type requestHeader struct {
	Version    uint8  `json:"version"`
	Operation  string `json:"operation"`
	DurationMS int64  `json:"duration_ms"`
	Bytes      int64  `json:"bytes,omitzero"`
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

	header, err := readRequestHeader(maxCtx, conn)
	if err != nil {
		return err
	}
	request, err := header.request()
	if err != nil {
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

	if err := writeAll(requestCtx, conn, []byte{ackByte}); err != nil {
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
	var ack [1]byte
	if err := readFull(operationCtx, conn, ack[:]); err != nil {
		return Result{}, err
	}
	if ack[0] != ackByte {
		return Result{}, protocolError(CodeInvalidRequest, errors.New("invalid diagnostic acknowledgement"))
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

func readRequestHeader(ctx context.Context, conn net.Conn) (requestHeader, error) {
	magic := make([]byte, len(wireMagic))
	if err := readFull(ctx, conn, magic); err != nil {
		return requestHeader{}, err
	}
	if string(magic) != string(wireMagic) {
		return requestHeader{}, protocolError(CodeInvalidMagic, errors.New("unexpected protocol magic"))
	}
	var frame [2]byte
	if err := readFull(ctx, conn, frame[:]); err != nil {
		return requestHeader{}, err
	}
	length := binary.BigEndian.Uint16(frame[:])
	if length > MaxHeaderBytes {
		return requestHeader{}, protocolError(CodeHeaderTooLarge, errors.New("header exceeds limit"))
	}
	body := make([]byte, length)
	limited := &io.LimitedReader{R: conn, N: int64(length)}
	if err := readFull(ctx, limited, body); err != nil {
		return requestHeader{}, err
	}
	var header requestHeader
	if err := json.Unmarshal(body, &header, json.RejectUnknownMembers(true)); err != nil {
		return requestHeader{}, protocolError(CodeMalformedHeader, err)
	}
	return header, nil
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
