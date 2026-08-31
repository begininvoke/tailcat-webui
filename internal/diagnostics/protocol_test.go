package diagnostics

import (
	"bytes"
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent/diagnosticrun"
	"github.com/ca-x/tailcat-webui/ent/enttest"

	_ "github.com/lib-x/entsqlite"
)

var diagnosticDatabaseSequence atomic.Uint64

func TestRunnerPing(t *testing.T) {
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- (Handler{}).Serve(t.Context(), server)
	}()

	runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), Request{Kind: RunKindPing, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != RunKindPing {
		t.Fatalf("kind = %q, want %q", result.Kind, RunKindPing)
	}
	if result.Latency < 0 {
		t.Fatalf("latency = %v, want non-negative", result.Latency)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerTreatsNilConnectionAsIOError(t *testing.T) {
	runner, err := NewRunner(func(context.Context) (net.Conn, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), Request{Kind: RunKindPing, Duration: time.Second})
	protocolErr, ok := errors.AsType[*ProtocolError](err)
	if !ok || protocolErr.Code != CodeIO {
		t.Fatalf("nil connection error = %v, want %s", err, CodeIO)
	}
}

func TestRunnerThroughputTransfersEachDirectionSequentially(t *testing.T) {
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- (Handler{}).Serve(t.Context(), server)
	}()

	runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), Request{
		Kind:     RunKindThroughput,
		Duration: time.Second,
		Bytes:    64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UploadBytes != 64<<10 || result.DownloadBytes != 64<<10 {
		t.Fatalf("transferred upload=%d download=%d, want 65536 each", result.UploadBytes, result.DownloadBytes)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRejectsSpoofedAndMalformedHeaders(t *testing.T) {
	tests := []struct {
		name  string
		magic []byte
		body  []byte
		code  ErrorCode
	}{
		{name: "spoofed magic", magic: []byte("BADMAGIC"), code: CodeInvalidMagic},
		{name: "malformed JSON", magic: wireMagic, body: []byte(`{"version":1`), code: CodeMalformedHeader},
		{name: "unknown operation", magic: wireMagic, body: []byte(`{"version":1,"operation":"dial","duration_ms":1}`), code: CodeInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			errCh := make(chan error, 1)
			go func() {
				defer server.Close()
				errCh <- (Handler{}).Serve(t.Context(), server)
			}()
			if _, err := client.Write(test.magic); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(test.magic, wireMagic) {
				client.Close()
				assertCode(t, <-errCh, test.code)
				return
			}
			if err := writeRawHeader(client, test.body); err != nil {
				t.Fatal(err)
			}
			client.Close()
			assertCode(t, <-errCh, test.code)
		})
	}
}

func TestHandlerRejectsOversizedHeaderAndRequestLimits(t *testing.T) {
	tests := []struct {
		name   string
		length uint16
		body   []byte
		code   ErrorCode
	}{
		{name: "header", length: MaxHeaderBytes + 1, code: CodeHeaderTooLarge},
		{name: "duration", body: []byte(`{"version":1,"operation":"ping","duration_ms":5001}`), code: CodeLimitExceeded},
		{name: "zero duration", body: []byte(`{"version":1,"operation":"ping","duration_ms":0}`), code: CodeInvalidRequest},
		{name: "bytes", body: []byte(`{"version":1,"operation":"throughput","duration_ms":1,"bytes":33554433}`), code: CodeLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			errCh := make(chan error, 1)
			go func() {
				defer server.Close()
				errCh <- (Handler{}).Serve(t.Context(), server)
			}()
			if _, err := client.Write(wireMagic); err != nil {
				t.Fatal(err)
			}
			if test.length != 0 {
				var frame [2]byte
				binary.BigEndian.PutUint16(frame[:], test.length)
				if _, err := client.Write(frame[:]); err != nil {
					t.Fatal(err)
				}
			} else if err := writeRawHeader(client, test.body); err != nil {
				t.Fatal(err)
			}
			client.Close()
			assertCode(t, <-errCh, test.code)
		})
	}
}

func TestRunnerRejectsLimitsBeforeDialing(t *testing.T) {
	runner, err := NewRunner(func(context.Context) (net.Conn, error) {
		t.Fatal("dial must not be called for invalid limits")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		request Request
		code    ErrorCode
	}{
		{request: Request{Kind: RunKindPing, Duration: 0}, code: CodeInvalidRequest},
		{request: Request{Kind: RunKindPing, Duration: MaxDuration + time.Nanosecond}, code: CodeLimitExceeded},
		{request: Request{Kind: RunKindThroughput, Duration: time.Second, Bytes: MaxBytesPerDirection + 1}, code: CodeLimitExceeded},
	} {
		_, err := runner.Run(t.Context(), test.request)
		assertCode(t, err, test.code)
	}
}

func TestHandlerReturnsStableErrorResponse(t *testing.T) {
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- (Handler{}).Serve(t.Context(), server)
	}()
	if _, err := client.Write(wireMagic); err != nil {
		t.Fatal(err)
	}
	if err := writeRawHeader(client, []byte(`{"version":1,"operation":"ping","duration_ms":0}`)); err != nil {
		t.Fatal(err)
	}
	response, err := readRawResponse(client)
	if err != nil {
		t.Fatal(err)
	}
	if response.Version != protocolVersion || response.Status != responseStatusError || response.Code != CodeInvalidRequest {
		t.Fatalf("response = %+v, want versioned invalid-request error", response)
	}
	assertCode(t, <-errCh, CodeInvalidRequest)
}

func TestRunnerPreservesRemoteStableError(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		defer server.Close()
		if _, _, err := readRequestHeader(t.Context(), server); err != nil {
			return
		}
		_ = writeRawResponse(server, responseHeader{Version: protocolVersion, Status: responseStatusError, Code: CodeLimitExceeded})
	}()
	runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), Request{Kind: RunKindPing, Duration: time.Second})
	assertCode(t, err, CodeLimitExceeded)
}

func TestRunnerRejectsMalformedAndOversizedResponse(t *testing.T) {
	tests := []struct {
		name  string
		write func(net.Conn) error
		code  ErrorCode
	}{
		{
			name:  "malformed error response",
			write: func(conn net.Conn) error { return writeRawResponseBytes(conn, []byte(`{"v":1,"s":"error"`)) },
			code:  CodeMalformedHeader,
		},
		{
			name: "oversized error response",
			write: func(conn net.Conn) error {
				var frame [2]byte
				binary.BigEndian.PutUint16(frame[:], MaxHeaderBytes+1)
				_, err := conn.Write(frame[:])
				return err
			},
			code: CodeHeaderTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			go func() {
				defer server.Close()
				if _, _, err := readRequestHeader(t.Context(), server); err == nil {
					_ = test.write(server)
				}
			}()
			runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), Request{Kind: RunKindPing, Duration: time.Second})
			assertCode(t, err, test.code)
		})
	}
}

func TestDiagnosticRunRestrictsPersistedErrorCodes(t *testing.T) {
	databaseName := fmt.Sprintf("file:diagnostics-error-code-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", diagnosticDatabaseSequence.Add(1))
	db := enttest.Open(t, "sqlite3", databaseName)
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tco…").SaveX(t.Context())
	if _, err := db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind("ping").SetErrorCode(diagnosticrun.ErrorCodeDiagnosticTimeout).Save(t.Context()); err != nil {
		t.Fatalf("save stable code: %v", err)
	}
	if _, err := db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind("ping").SetErrorCode(diagnosticrun.ErrorCode("198.51.100.7")).Save(t.Context()); err == nil {
		t.Fatal("save arbitrary error code succeeded")
	}
}

func TestRunnerHonorsCancellationAndSilentPeerTimeout(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		startedWrite := make(chan struct{})
		go func() {
			var firstByte [1]byte
			if _, err := io.ReadFull(server, firstByte[:]); err == nil {
				close(startedWrite)
			}
		}()
		runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		resultCh := make(chan error, 1)
		go func() {
			_, runErr := runner.Run(ctx, Request{Kind: RunKindPing, Duration: time.Second})
			resultCh <- runErr
		}()
		<-startedWrite
		cancel()
		err = <-resultCh
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
		assertCode(t, err, CodeCanceled)
	})

	t.Run("silent peer", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
		if err != nil {
			t.Fatal(err)
		}
		_, err = runner.Run(t.Context(), Request{Kind: RunKindPing, Duration: 20 * time.Millisecond})
		assertCode(t, err, CodeTimeout)
	})
}

func TestRunnerHonorsCancellationWhileReadingDownload(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	readUpload := make(chan struct{})
	go func() {
		header, _, err := readRequestHeader(context.Background(), server)
		if err != nil {
			return
		}
		request, err := header.request()
		if err != nil {
			return
		}
		if err := writeRawResponse(server, responseHeader{Version: protocolVersion, Status: responseStatusOK}); err != nil {
			return
		}
		if err := discardExact(context.Background(), server, request.Bytes); err == nil {
			close(readUpload)
		}
	}()
	runner, err := NewRunner(func(context.Context) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	resultCh := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, Request{Kind: RunKindThroughput, Duration: time.Second, Bytes: 1024})
		resultCh <- runErr
	}()
	<-readUpload
	cancel()
	assertCode(t, <-resultCh, CodeCanceled)
}

func TestHandlerHonorsCancellationDuringPartialBody(t *testing.T) {
	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		errCh <- (Handler{}).Serve(ctx, server)
	}()
	if _, err := client.Write(wireMagic); err != nil {
		t.Fatal(err)
	}
	if err := writeRawHeader(client, []byte(`{"version":1,"operation":"throughput","duration_ms":1000,"bytes":1024}`)); err != nil {
		t.Fatal(err)
	}
	response, err := readRawResponse(client)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != responseStatusOK {
		t.Fatalf("response status = %q, want %q", response.Status, responseStatusOK)
	}
	if _, err := client.Write(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	assertCode(t, <-errCh, CodeCanceled)
}

func writeRawHeader(conn net.Conn, body []byte) error {
	if len(body) > int(^uint16(0)) {
		return errors.New("header too large for test frame")
	}
	var frame [2]byte
	binary.BigEndian.PutUint16(frame[:], uint16(len(body)))
	if _, err := conn.Write(frame[:]); err != nil {
		return err
	}
	_, err := conn.Write(body)
	return err
}

func writeRawResponse(conn net.Conn, response responseHeader) error {
	body, err := json.Marshal(&response)
	if err != nil {
		return err
	}
	return writeRawResponseBytes(conn, body)
}

func writeRawResponseBytes(conn net.Conn, body []byte) error {
	return writeRawHeader(conn, body)
}

func readRawResponse(conn net.Conn) (responseHeader, error) {
	var frame [2]byte
	if _, err := io.ReadFull(conn, frame[:]); err != nil {
		return responseHeader{}, err
	}
	body := make([]byte, binary.BigEndian.Uint16(frame[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return responseHeader{}, err
	}
	var response responseHeader
	if err := json.Unmarshal(body, &response); err != nil {
		return responseHeader{}, err
	}
	return response, nil
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	protocolErr, ok := errors.AsType[*ProtocolError](err)
	if !ok {
		t.Fatalf("error = %T (%v), want ProtocolError with code %q", err, err, want)
	}
	if protocolErr.Code != want {
		t.Fatalf("code = %q, want %q (error: %v)", protocolErr.Code, want, err)
	}
}
