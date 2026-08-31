package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/diagnosticrun"
	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/events"

	_ "github.com/lib-x/entsqlite"
)

var serviceDatabaseSequence atomic.Uint64

var errInjectedLifecycle = errors.New("injected diagnostic lifecycle failure")

type dialPortFunc func(context.Context, string, string, uint16) (net.Conn, error)

func (f dialPortFunc) DialPort(ctx context.Context, ownerID, clientID string, port uint16) (net.Conn, error) {
	return f(ctx, ownerID, clientID, port)
}

type auditRecorderFunc func(context.Context, audit.Entry) error

func (f auditRecorderFunc) Record(ctx context.Context, entry audit.Entry) error {
	return f(ctx, entry)
}

type eventPublisherFunc func(string, string, events.RuntimePhase, EventPayload)

func (f eventPublisherFunc) PublishDiagnostic(ownerID, runID string, phase events.RuntimePhase, payload EventPayload) {
	f(ownerID, runID, phase, payload)
}

type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestServiceCreatesRunningSummaryBeforeDial(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	dialed := make(chan struct{})
	dialer := dialPortFunc(func(ctx context.Context, ownerID, clientID string, port uint16) (net.Conn, error) {
		if ownerID != owner.ID || clientID != client.ID || port != ReservedPort {
			t.Errorf("DialPort(%q, %q, %d)", ownerID, clientID, port)
		}
		run := db.DiagnosticRun.Query().Where(
			diagnosticrun.UserIDEQ(owner.ID),
			diagnosticrun.ClientIDEQ(client.ID),
		).OnlyX(t.Context())
		if run.Status != diagnosticrun.StatusRunning || run.FinishedAt != nil {
			t.Errorf("summary at dial = status %q, finished_at %v", run.Status, run.FinishedAt)
		}
		close(dialed)
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			_ = (Handler{}).Serve(ctx, server)
		}()
		return peer, nil
	})
	service := newServiceForTest(t, db, dialer)

	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{
		Kind:     RunKindPing,
		Duration: time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic was not dialed")
	}
	finished := waitForRunStatus(t, service, owner.ID, run.ID, RunStatusSucceeded)
	if finished.FinishedAt == nil || finished.LatencyMS == nil || finished.ErrorCode != "" {
		t.Fatalf("finished run = %+v", finished)
	}
}

func TestServiceRejectsNilDependencies(t *testing.T) {
	db, _, _ := newServiceTestData(t)
	dialer := blockedDialer(t)
	recorder := auditRecorderFunc(func(context.Context, audit.Entry) error { return nil })
	publisher := eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name      string
		db        *ent.Client
		dialer    ClientDialer
		recorder  AuditRecorder
		publisher EventPublisher
		logger    *slog.Logger
	}{
		{"database", nil, dialer, recorder, publisher, logger},
		{"dialer", db, nil, recorder, publisher, logger},
		{"auditor", db, dialer, nil, publisher, logger},
		{"publisher", db, dialer, recorder, nil, logger},
		{"logger", db, dialer, recorder, publisher, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewService(t.Context(), test.db, test.dialer, test.recorder, test.publisher, test.logger); err == nil {
				t.Fatal("nil dependency was accepted")
			}
		})
	}
}

func TestServicePersistsThroughputTerminalSummary(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	dialer := dialPortFunc(func(ctx context.Context, _, _ string, port uint16) (net.Conn, error) {
		if port != ReservedPort {
			t.Errorf("dial port = %d", port)
		}
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			_ = (Handler{}).Serve(ctx, server)
		}()
		return peer, nil
	})
	service := newServiceForTest(t, db, dialer)
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindThroughput, Duration: time.Second, Bytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitForRunStatus(t, service, owner.ID, run.ID, RunStatusSucceeded)
	if finished.UploadBytes != 64<<10 || finished.DownloadBytes != 64<<10 || finished.UploadBPS <= 0 || finished.DownloadBPS <= 0 || finished.ErrorCode != "" {
		t.Fatalf("throughput summary = %+v", finished)
	}
}

func TestServiceStartDoesNotDialWhenAuditCannotBeRecorded(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	var auditCalls atomic.Int64
	var dialCalls atomic.Int64
	recorder := auditRecorderFunc(func(context.Context, audit.Entry) error {
		auditCalls.Add(1)
		return errInjectedLifecycle
	})
	dialer := dialPortFunc(func(context.Context, string, string, uint16) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errInjectedLifecycle
	})
	service, err := NewService(t.Context(), db, dialer, recorder, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	_, startErr := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second})
	closeErr := service.Close()
	if !errors.Is(startErr, errInjectedLifecycle) {
		t.Errorf("Start error = %v, want injected audit failure", startErr)
	}
	if got := auditCalls.Load(); got != 3 {
		t.Errorf("audit attempts = %d, want 3", got)
	}
	if got := dialCalls.Load(); got != 0 {
		t.Errorf("dial attempts = %d, want 0", got)
	}
	row := db.DiagnosticRun.Query().OnlyX(t.Context())
	if row.Status != diagnosticrun.StatusRunning || row.FinishedAt != nil {
		t.Errorf("unresolved durable row = %+v", row)
	}
	if !errors.Is(closeErr, errInjectedLifecycle) {
		t.Errorf("Close error = %v, want unresolved audit failure", closeErr)
	}
	healthyAudit, err := audit.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := NewService(t.Context(), db, blockedDialer(t), healthyAudit, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reconcile failed start lifecycle: %v", err)
	}
	t.Cleanup(func() { _ = reconciled.Close() })
	row = db.DiagnosticRun.GetX(t.Context(), row.ID)
	if row.Status != diagnosticrun.StatusInterrupted || row.FinishedAt == nil {
		t.Fatalf("reconciled failed start row = %+v", row)
	}
	audits := db.AuditEvent.Query().AllX(t.Context())
	if len(audits) != 2 || audits[0].ResourceID != row.ID || audits[1].ResourceID != row.ID {
		t.Fatalf("reconciled failed start audits = %+v", audits)
	}
	actions := map[string]bool{audits[0].Action: true, audits[1].Action: true}
	if !actions["diagnostic.start"] || !actions["diagnostic.interrupted"] {
		t.Fatalf("reconciled failed start actions = %+v", actions)
	}
}

func TestServiceRetriesTransientTerminalPersistenceFailure(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	var remaining atomic.Int64
	remaining.Store(2)
	var attempts atomic.Int64
	db.DiagnosticRun.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if diagnosticMutation, ok := mutation.(*ent.DiagnosticRunMutation); ok {
				if status, exists := diagnosticMutation.Status(); exists && status != diagnosticrun.StatusRunning {
					attempts.Add(1)
					if remaining.Load() > 0 {
						remaining.Add(-1)
						return nil, errInjectedLifecycle
					}
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	runnerClosed := make(chan struct{})
	service := newServiceForTest(t, db, successfulNotifyingDialer(runnerClosed))
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-runnerClosed
	closeErr := service.Close()
	if closeErr != nil {
		t.Fatalf("Close after transient persistence failure: %v", closeErr)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("terminal persistence attempts = %d, want 3", got)
	}
	row := db.DiagnosticRun.GetX(t.Context(), run.ID)
	if row.Status == diagnosticrun.StatusRunning || row.FinishedAt == nil {
		t.Fatalf("terminal row after retry = %+v", row)
	}
}

func TestServiceSurfacesTerminalPersistenceFailureAndRecoversDurableRow(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	var failPersistence atomic.Bool
	failPersistence.Store(true)
	var attempts atomic.Int64
	db.DiagnosticRun.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if diagnosticMutation, ok := mutation.(*ent.DiagnosticRunMutation); ok {
				if status, exists := diagnosticMutation.Status(); exists && status != diagnosticrun.StatusRunning && failPersistence.Load() {
					attempts.Add(1)
					return nil, errInjectedLifecycle
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	recorder := auditRecorderFunc(func(context.Context, audit.Entry) error { return nil })
	publisher := eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {})
	runnerClosed := make(chan struct{})
	service, err := NewService(t.Context(), db, successfulNotifyingDialer(runnerClosed), recorder, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-runnerClosed
	closeErr := service.Close()
	if !errors.Is(closeErr, errInjectedLifecycle) {
		t.Fatalf("Close error = %v, want terminal persistence failure", closeErr)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("terminal persistence attempts = %d, want 3", got)
	}
	stale := db.DiagnosticRun.GetX(t.Context(), run.ID)
	if stale.Status != diagnosticrun.StatusRunning || stale.FinishedAt != nil {
		t.Fatalf("unresolved durable row = %+v", stale)
	}

	failPersistence.Store(false)
	recoveredService, err := NewService(t.Context(), db, blockedDialer(t), recorder, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	t.Cleanup(func() { _ = recoveredService.Close() })
	recovered := db.DiagnosticRun.GetX(t.Context(), run.ID)
	if recovered.Status != diagnosticrun.StatusInterrupted || recovered.FinishedAt == nil {
		t.Fatalf("recovered durable row = %+v", recovered)
	}
}

func TestServiceRetriesTransientTerminalAuditFailure(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	auditService, err := audit.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	var remaining atomic.Int64
	remaining.Store(2)
	var terminalCalls atomic.Int64
	recorder := auditRecorderFunc(func(ctx context.Context, entry audit.Entry) error {
		if entry.Action != "diagnostic.start" {
			terminalCalls.Add(1)
			if remaining.Load() > 0 {
				remaining.Add(-1)
				return errInjectedLifecycle
			}
		}
		return auditService.Record(ctx, entry)
	})
	runnerClosed := make(chan struct{})
	service, err := NewService(t.Context(), db, successfulNotifyingDialer(runnerClosed), recorder, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	<-runnerClosed
	if err := service.Close(); err != nil {
		t.Fatalf("Close after transient terminal audit failure: %v", err)
	}
	if got := terminalCalls.Load(); got != 3 {
		t.Fatalf("terminal audit attempts = %d, want 3", got)
	}
	if got := db.AuditEvent.Query().CountX(t.Context()); got != 2 {
		t.Fatalf("durable audit rows = %d, want start and terminal", got)
	}
}

func TestServiceSurfacesPermanentTerminalAuditFailureFromClose(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	auditService, err := audit.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	var terminalCalls atomic.Int64
	recorder := auditRecorderFunc(func(ctx context.Context, entry audit.Entry) error {
		if entry.Action != "diagnostic.start" {
			terminalCalls.Add(1)
			return errInjectedLifecycle
		}
		return auditService.Record(ctx, entry)
	})
	runnerClosed := make(chan struct{})
	service, err := NewService(t.Context(), db, successfulNotifyingDialer(runnerClosed), recorder, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-runnerClosed
	closeErr := service.Close()
	if !errors.Is(closeErr, errInjectedLifecycle) {
		t.Fatalf("Close error = %v, want terminal audit failure", closeErr)
	}
	if got := terminalCalls.Load(); got != 3 {
		t.Fatalf("terminal audit attempts = %d, want 3", got)
	}
	if got := db.AuditEvent.Query().CountX(t.Context()); got != 1 {
		t.Fatalf("durable audit rows = %d, want only start", got)
	}
	terminalRow := db.DiagnosticRun.GetX(t.Context(), run.ID)
	if terminalRow.Status == diagnosticrun.StatusRunning || terminalRow.FinishedAt == nil {
		t.Fatalf("terminal row was not persisted before audit failure: %+v", terminalRow)
	}
	_, reconciliationErr := NewService(t.Context(), db, blockedDialer(t), recorder, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(reconciliationErr, errInjectedLifecycle) {
		t.Fatalf("NewService with unavailable auditor error = %v", reconciliationErr)
	}
	if got := terminalCalls.Load(); got != 6 {
		t.Fatalf("terminal audit attempts after startup reconciliation = %d, want 6", got)
	}
	if got := db.AuditEvent.Query().CountX(t.Context()); got != 1 {
		t.Fatalf("audit rows after unavailable reconciliation = %d, want only start", got)
	}
	if preserved := db.DiagnosticRun.GetX(t.Context(), run.ID); preserved.Status != terminalRow.Status || preserved.FinishedAt == nil {
		t.Fatalf("terminal row changed during failed reconciliation: %+v", preserved)
	}

	healthyAudit, err := audit.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := NewService(t.Context(), db, blockedDialer(t), healthyAudit, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("healthy startup reconciliation: %v", err)
	}
	if err := reconciled.Close(); err != nil {
		t.Fatal(err)
	}
	audits := db.AuditEvent.Query().AllX(t.Context())
	if len(audits) != 2 {
		t.Fatalf("audits after healthy reconciliation = %+v", audits)
	}
	wantTerminalAction := "diagnostic." + terminalRow.Status.String()
	actions := map[string]bool{audits[0].Action: true, audits[1].Action: true}
	if !actions["diagnostic.start"] || !actions[wantTerminalAction] {
		t.Fatalf("reconciled terminal actions = %+v, want %q", actions, wantTerminalAction)
	}
	secondRestart, err := NewService(t.Context(), db, blockedDialer(t), healthyAudit, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("second startup reconciliation: %v", err)
	}
	if err := secondRestart.Close(); err != nil {
		t.Fatal(err)
	}
	if got := db.AuditEvent.Query().CountX(t.Context()); got != 2 {
		t.Fatalf("audit rows after second restart = %d, want 2", got)
	}
}

func TestServiceSurfacesRecoveryAuditFailure(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	stale := db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind(diagnosticrun.KindPing).SetStatus(diagnosticrun.StatusRunning).SaveX(t.Context())
	var calls atomic.Int64
	recorder := auditRecorderFunc(func(context.Context, audit.Entry) error {
		calls.Add(1)
		return errInjectedLifecycle
	})
	_, err := NewService(t.Context(), db, blockedDialer(t), recorder, eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, errInjectedLifecycle) {
		t.Fatalf("NewService recovery error = %v, want audit failure", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("recovery audit attempts = %d, want 3", got)
	}
	recovered := db.DiagnosticRun.GetX(t.Context(), stale.ID)
	if recovered.Status != diagnosticrun.StatusRunning || recovered.FinishedAt != nil {
		t.Fatalf("recovery audit failure consumed durable retry marker: %+v", recovered)
	}
	retryService, err := NewService(
		t.Context(), db, blockedDialer(t),
		auditRecorderFunc(func(context.Context, audit.Entry) error { return nil }),
		eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	t.Cleanup(func() { _ = retryService.Close() })
	if retried := db.DiagnosticRun.GetX(t.Context(), stale.ID); retried.Status != diagnosticrun.StatusInterrupted || retried.FinishedAt == nil {
		t.Fatalf("retried recovery row = %+v", retried)
	}
}

func TestServiceSimultaneousStartsReserveOneRunPerClient(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	service := newServiceForTest(t, db, blockedDialer(t))
	const attempts = 24
	ready := make(chan struct{})
	errorsCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() {
			<-ready
			_, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
			errorsCh <- err
		})
	}
	close(ready)
	wg.Wait()
	close(errorsCh)
	succeeded, rejected := 0, 0
	for err := range errorsCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrClientActive):
			rejected++
		default:
			t.Fatalf("Start error = %v", err)
		}
	}
	if succeeded != 1 || rejected != attempts-1 {
		t.Fatalf("simultaneous starts: succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestServiceSimultaneousStartsReserveTwoRunsPerOwner(t *testing.T) {
	db, owner, first := newServiceTestData(t)
	clients := []*ent.TailClient{first}
	for i := 1; i < 12; i++ {
		clients = append(clients, db.TailClient.Create().SetUserID(owner.ID).SetName(fmt.Sprintf("client-%d", i)).SetServerTokenCipher([]byte("token")).SetTokenHint("token").SaveX(t.Context()))
	}
	service := newServiceForTest(t, db, blockedDialer(t))
	ready := make(chan struct{})
	errorsCh := make(chan error, len(clients))
	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Go(func() {
			<-ready
			_, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
			errorsCh <- err
		})
	}
	close(ready)
	wg.Wait()
	close(errorsCh)
	succeeded, rejected := 0, 0
	for err := range errorsCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOwnerCapacity):
			rejected++
		default:
			t.Fatalf("Start error = %v", err)
		}
	}
	if succeeded != 2 || rejected != len(clients)-2 {
		t.Fatalf("simultaneous starts: succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestServiceOwnerScopeHidesClientsAndRuns(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	other := db.User.Create().SetIssuer("test").SetSubject("other-owner").SaveX(t.Context())
	service := newServiceForTest(t, db, blockedDialer(t))

	if _, err := service.Start(t.Context(), other.ID, client.ID, StartInput{Kind: "invalid"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Start error = %v, want not found", err)
	}
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := service.List(t.Context(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("cross-owner List returned %+v", runs)
	}
	if err := service.Cancel(t.Context(), other.ID, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Cancel error = %v, want not found", err)
	}
}

func TestServiceCancellationFreesClientAndOwnerReservations(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	service := newServiceForTest(t, db, blockedDialer(t))
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(t.Context(), owner.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, service, owner.ID, run.ID, RunStatusCanceled)
	if _, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration}); err != nil {
		t.Fatalf("Start after cancellation: %v", err)
	}
}

func TestServiceTerminalCompareAndSetAuditsAndPublishesOnce(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	var mu sync.Mutex
	var entries []audit.Entry
	var published []EventPayload
	recorder := auditRecorderFunc(func(_ context.Context, entry audit.Entry) error {
		mu.Lock()
		defer mu.Unlock()
		entries = append(entries, entry)
		return nil
	})
	publisher := eventPublisherFunc(func(ownerID, runID string, phase events.RuntimePhase, payload EventPayload) {
		if ownerID != owner.ID || runID == "" || phase == "" {
			t.Errorf("PublishDiagnostic(%q, %q, %q)", ownerID, runID, phase)
		}
		mu.Lock()
		defer mu.Unlock()
		published = append(published, payload)
	})
	service, err := NewService(t.Context(), db, blockedDialer(t), recorder, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
	if err != nil {
		t.Fatal(err)
	}

	terminalErr := &ProtocolError{Code: CodeIO, cause: io.ErrUnexpectedEOF}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { service.finish(run.ID, Result{}, terminalErr) })
	}
	wg.Wait()
	waitForRunStatus(t, service, owner.ID, run.ID, RunStatusFailed)

	mu.Lock()
	defer mu.Unlock()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %+v, want start and one terminal", entries)
	}
	if entries[0].Action != "diagnostic.start" || entries[1].Action != "diagnostic.failed" {
		t.Fatalf("audit actions = %q, %q", entries[0].Action, entries[1].Action)
	}
	for _, entry := range entries {
		if entry.UserID != owner.ID || entry.ResourceKind != "diagnostic" || entry.ResourceID != run.ID || entry.Detail != "client_id="+client.ID {
			t.Errorf("audit identity = %+v", entry)
		}
	}
	if len(published) != 2 || published[0].Status != RunStatusRunning || published[1].Status != RunStatusFailed || published[1].ClientID != client.ID || published[1].Progress != 100 {
		t.Fatalf("published payloads = %+v", published)
	}
}

func TestServiceCloseDrainsAndInterruptsRuns(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	service := newServiceForTest(t, db, blockedDialer(t))
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: MaxDuration})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	finished := waitForRunStatus(t, service, owner.ID, run.ID, RunStatusInterrupted)
	if finished.ErrorCode != CodeIO {
		t.Fatalf("interrupted error code = %q, want %q", finished.ErrorCode, CodeIO)
	}
	if _, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close error = %v", err)
	}
}

func TestServiceCloseWaitsForStartOwnershipQuery(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	service := newServiceForTest(t, db, blockedDialer(t))
	queryStarted := make(chan struct{})
	releaseQuery := make(chan struct{})
	var interceptOnce sync.Once
	db.TailClient.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			interceptOnce.Do(func() {
				close(queryStarted)
				<-releaseQuery
			})
			return next.Query(ctx, query)
		})
	}))

	startResult := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: time.Second})
		startResult <- err
	})
	<-queryStarted
	service.mu.Lock()
	pending := service.pending
	service.mu.Unlock()
	if pending != 1 {
		close(releaseQuery)
		wg.Wait()
		t.Fatalf("pending operations during ownership query = %d, want 1", pending)
	}

	closeResult := make(chan error, 1)
	wg.Go(func() { closeResult <- service.Close() })
	for {
		service.mu.Lock()
		closed := service.closed
		service.mu.Unlock()
		if closed {
			break
		}
		runtime.Gosched()
	}
	select {
	case err := <-closeResult:
		close(releaseQuery)
		wg.Wait()
		t.Fatalf("Close returned during ownership query: %v", err)
	default:
	}
	close(releaseQuery)
	if err := <-startResult; !errors.Is(err, ErrClosed) {
		t.Fatalf("Start racing Close error = %v, want closed", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestServiceStartupInterruptsStaleRunningRows(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	stale := db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind(diagnosticrun.KindPing).SetStatus(diagnosticrun.StatusRunning).SaveX(t.Context())
	var entries []audit.Entry
	var payloads []EventPayload
	recorder := auditRecorderFunc(func(_ context.Context, entry audit.Entry) error {
		entries = append(entries, entry)
		return nil
	})
	publisher := eventPublisherFunc(func(_ string, _ string, _ events.RuntimePhase, payload EventPayload) {
		payloads = append(payloads, payload)
	})
	service, err := NewService(t.Context(), db, blockedDialer(t), recorder, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	recovered := db.DiagnosticRun.GetX(t.Context(), stale.ID)
	if recovered.Status != diagnosticrun.StatusInterrupted || recovered.FinishedAt == nil || recovered.ErrorCode != diagnosticrun.ErrorCodeDiagnosticIo {
		t.Fatalf("recovered row = %+v", recovered)
	}
	if len(entries) != 2 || entries[0].Action != "diagnostic.start" || entries[1].Action != "diagnostic.interrupted" || entries[0].Detail != "client_id="+client.ID || entries[1].Detail != "client_id="+client.ID {
		t.Fatalf("recovery audits = %+v", entries)
	}
	if len(payloads) != 1 || payloads[0].Status != RunStatusInterrupted || payloads[0].ClientID != client.ID {
		t.Fatalf("recovery events = %+v", payloads)
	}
}

func TestServiceRetentionIsOwnerScopedByAgeAndCount(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	other := db.User.Create().SetIssuer("test").SetSubject("retention-other").SaveX(t.Context())
	otherClient := db.TailClient.Create().SetUserID(other.ID).SetName("other").SetServerTokenCipher([]byte("token")).SetTokenHint("token").SaveX(t.Context())
	now := time.Now()
	for i := range 105 {
		db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind(diagnosticrun.KindPing).SetStatus(diagnosticrun.StatusSucceeded).SetStartedAt(now.Add(-time.Duration(i) * time.Minute)).SaveX(t.Context())
	}
	old := db.DiagnosticRun.Create().SetUserID(owner.ID).SetClientID(client.ID).SetKind(diagnosticrun.KindPing).SetStatus(diagnosticrun.StatusSucceeded).SetStartedAt(now.Add(-31 * 24 * time.Hour)).SaveX(t.Context())
	otherOld := db.DiagnosticRun.Create().SetUserID(other.ID).SetClientID(otherClient.ID).SetKind(diagnosticrun.KindPing).SetStatus(diagnosticrun.StatusSucceeded).SetStartedAt(now.Add(-31 * 24 * time.Hour)).SaveX(t.Context())
	service := newServiceForTest(t, db, blockedDialer(t))

	runs, err := service.List(t.Context(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != retainedRunsPerOwner {
		t.Fatalf("retained list count = %d, want %d", len(runs), retainedRunsPerOwner)
	}
	ownerRows := db.DiagnosticRun.Query().Where(diagnosticrun.UserIDEQ(owner.ID)).AllX(t.Context())
	if len(ownerRows) != retainedRunsPerOwner {
		t.Fatalf("durable owner rows = %d, want %d", len(ownerRows), retainedRunsPerOwner)
	}
	ownerIDs := make([]string, 0, len(ownerRows))
	for _, row := range ownerRows {
		ownerIDs = append(ownerIDs, row.ID)
	}
	if slices.Contains(ownerIDs, old.ID) {
		t.Fatal("row older than 30 days was retained")
	}
	if !db.DiagnosticRun.Query().Where(diagnosticrun.IDEQ(otherOld.ID)).ExistX(t.Context()) {
		t.Fatal("owner retention deleted another owner's row")
	}
}

func TestServiceLiveProgressIsDroppableAndPublishedAtMostOncePerSecond(t *testing.T) {
	db, owner, client := newServiceTestData(t)
	var mu sync.Mutex
	var progressAt []time.Time
	publisher := eventPublisherFunc(func(_ string, _ string, _ events.RuntimePhase, payload EventPayload) {
		if payload.Progress > 0 && payload.Progress < 100 {
			mu.Lock()
			progressAt = append(progressAt, time.Now())
			mu.Unlock()
		}
	})
	service, err := NewService(
		t.Context(), db, blockedDialer(t),
		auditRecorderFunc(func(context.Context, audit.Entry) error { return nil }),
		publisher,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	run, err := service.Start(t.Context(), owner.ID, client.ID, StartInput{Kind: RunKindPing, Duration: 2300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, service, owner.ID, run.ID, RunStatusFailed)
	mu.Lock()
	defer mu.Unlock()
	if len(progressAt) != 2 {
		t.Fatalf("live progress events = %d, want 2", len(progressAt))
	}
	if gap := progressAt[1].Sub(progressAt[0]); gap < 900*time.Millisecond {
		t.Fatalf("progress gap = %s, want at least 900ms", gap)
	}
	row := db.DiagnosticRun.GetX(t.Context(), run.ID)
	if row.UploadBytes != 0 || row.DownloadBytes != 0 {
		t.Fatalf("live samples leaked into durable summary: %+v", row)
	}
}

func blockedDialer(t *testing.T) ClientDialer {
	t.Helper()
	return dialPortFunc(func(context.Context, string, string, uint16) (net.Conn, error) {
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		return peer, nil
	})
}

func successfulNotifyingDialer(runnerClosed chan struct{}) ClientDialer {
	return dialPortFunc(func(ctx context.Context, _, _ string, _ uint16) (net.Conn, error) {
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			_ = (Handler{}).Serve(ctx, server)
		}()
		return &closeNotifyConn{Conn: peer, closed: runnerClosed}, nil
	})
}

func newServiceTestData(t *testing.T) (*ent.Client, *ent.User, *ent.TailClient) {
	t.Helper()
	databaseName := fmt.Sprintf("file:diagnostics-service-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", serviceDatabaseSequence.Add(1))
	db := enttest.Open(t, "sqlite3", databaseName)
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("token")).SetTokenHint("token").SaveX(t.Context())
	return db, owner, client
}

func newServiceForTest(t *testing.T, db *ent.Client, dialer ClientDialer) *Service {
	t.Helper()
	recorder := auditRecorderFunc(func(context.Context, audit.Entry) error { return nil })
	publisher := eventPublisherFunc(func(string, string, events.RuntimePhase, EventPayload) {})
	service, err := NewService(t.Context(), db, dialer, recorder, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func waitForRunStatus(t *testing.T, service *Service, ownerID, runID string, status RunStatus) RunView {
	t.Helper()
	deadline := time.Now().Add(MaxDuration + 2*time.Second)
	for time.Now().Before(deadline) {
		runs, err := service.List(t.Context(), ownerID)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.ID == runID && run.Status == status {
				return run
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s", runID, status)
	return RunView{}
}
