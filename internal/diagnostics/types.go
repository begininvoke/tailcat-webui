// Package diagnostics defines the bounded Tailcat peer diagnostic protocol and
// the durable values used to summarize its runs.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	// ReservedPort is the only Tailcat TCP port that serves diagnostics.
	ReservedPort uint16 = 41640

	// MaxDuration and MaxBytesPerDirection bound all peer-controlled work.
	MaxDuration          = 5 * time.Second
	MaxBytesPerDirection = int64(32 << 20)
	MaxHeaderBytes       = 1024
)

type RunKind string

const (
	RunKindPing       RunKind = "ping"
	RunKindThroughput RunKind = "throughput"
)

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusSucceeded   RunStatus = "succeeded"
	RunStatusFailed      RunStatus = "failed"
	RunStatusCanceled    RunStatus = "canceled"
	RunStatusInterrupted RunStatus = "interrupted"
)

type ErrorCode string

const (
	CodeCanceled        ErrorCode = "diagnostic_canceled"
	CodeTimeout         ErrorCode = "diagnostic_timeout"
	CodeInvalidMagic    ErrorCode = "diagnostic_invalid_magic"
	CodeHeaderTooLarge  ErrorCode = "diagnostic_header_too_large"
	CodeMalformedHeader ErrorCode = "diagnostic_malformed_header"
	CodeInvalidRequest  ErrorCode = "diagnostic_invalid_request"
	CodeLimitExceeded   ErrorCode = "diagnostic_limit_exceeded"
	CodeIO              ErrorCode = "diagnostic_io"
	CodeInvalidRunner   ErrorCode = "diagnostic_invalid_runner"
)

// ProtocolError has a stable machine code while preserving the underlying
// cancellation or transport error for errors.Is and errors.AsType callers.
type ProtocolError struct {
	Code  ErrorCode
	cause error
}

func (e *ProtocolError) Error() string { return string(e.Code) }

func (e *ProtocolError) Unwrap() error { return e.cause }

// Request is intentionally limited to a fixed operation and bounded work. It
// has no target address because the selected Tailcat peer is the only target.
type Request struct {
	Kind     RunKind
	Duration time.Duration
	Bytes    int64
}

// Result contains transient protocol measurements. The service turns these
// into a durable summary and deliberately does not persist progress samples.
type Result struct {
	Kind          RunKind
	Latency       time.Duration
	Duration      time.Duration
	UploadBytes   int64
	DownloadBytes int64
}

// DialFunc dials the already-selected diagnostic peer. It intentionally has no
// address argument, so this protocol cannot be used as a general dial endpoint.
type DialFunc func(context.Context) (net.Conn, error)

// Runner executes a bounded diagnostic against a preselected peer.
type Runner struct {
	dial DialFunc
}

func NewRunner(dial DialFunc) (Runner, error) {
	if dial == nil {
		return Runner{}, &ProtocolError{Code: CodeInvalidRunner, cause: errors.New("nil diagnostic dialer")}
	}
	return Runner{dial: dial}, nil
}

func protocolError(code ErrorCode, cause error) error {
	return &ProtocolError{Code: code, cause: cause}
}

func requestHeaderFor(request Request) (requestHeader, error) {
	duration := request.Duration
	if duration <= 0 {
		return requestHeader{}, protocolError(CodeInvalidRequest, fmt.Errorf("duration must be positive"))
	}
	if duration > MaxDuration {
		return requestHeader{}, protocolError(CodeLimitExceeded, fmt.Errorf("duration exceeds %s", MaxDuration))
	}
	if duration < time.Millisecond {
		return requestHeader{}, protocolError(CodeInvalidRequest, fmt.Errorf("duration is less than 1ms"))
	}

	header := requestHeader{
		Version:    protocolVersion,
		Operation:  string(request.Kind),
		DurationMS: duration.Milliseconds(),
		Bytes:      request.Bytes,
	}
	switch request.Kind {
	case RunKindPing:
		if request.Bytes != 0 {
			return requestHeader{}, protocolError(CodeInvalidRequest, fmt.Errorf("ping bytes must be zero"))
		}
	case RunKindThroughput:
		if request.Bytes <= 0 {
			return requestHeader{}, protocolError(CodeInvalidRequest, fmt.Errorf("throughput bytes must be positive"))
		}
		if request.Bytes > MaxBytesPerDirection {
			return requestHeader{}, protocolError(CodeLimitExceeded, fmt.Errorf("bytes exceeds %d", MaxBytesPerDirection))
		}
	default:
		return requestHeader{}, protocolError(CodeInvalidRequest, fmt.Errorf("unsupported operation %q", request.Kind))
	}
	return header, nil
}

func (c ErrorCode) valid() bool {
	switch c {
	case CodeCanceled, CodeTimeout, CodeInvalidMagic, CodeHeaderTooLarge,
		CodeMalformedHeader, CodeInvalidRequest, CodeLimitExceeded, CodeIO,
		CodeInvalidRunner:
		return true
	default:
		return false
	}
}

func (h requestHeader) request() (Request, error) {
	if h.Version != protocolVersion {
		return Request{}, protocolError(CodeInvalidRequest, fmt.Errorf("unsupported protocol version"))
	}
	if h.DurationMS <= 0 {
		return Request{}, protocolError(CodeInvalidRequest, fmt.Errorf("duration must be positive"))
	}
	if h.DurationMS > MaxDuration.Milliseconds() {
		return Request{}, protocolError(CodeLimitExceeded, fmt.Errorf("duration exceeds %s", MaxDuration))
	}
	request := Request{
		Kind:     RunKind(h.Operation),
		Duration: time.Duration(h.DurationMS) * time.Millisecond,
		Bytes:    h.Bytes,
	}
	if _, err := requestHeaderFor(request); err != nil {
		return Request{}, err
	}
	return request, nil
}
