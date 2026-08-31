package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
	"uuid"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/diagnosticrun"
	"github.com/ca-x/tailcat-webui/ent/tailclient"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/events"
)

const (
	maxActiveRunsPerOwner  = 2
	retainedRunsPerOwner   = 100
	auditReconcilePageSize = 100
	retentionAge           = 30 * 24 * time.Hour
	lifecycleRetryAttempts = 3
	lifecycleRetryDelay    = 10 * time.Millisecond
)

var (
	ErrNotFound      = errors.New("diagnostic resource not found")
	ErrInvalidKind   = errors.New("invalid diagnostic kind")
	ErrInvalidLimits = errors.New("invalid diagnostic limits")
	ErrClientActive  = errors.New("diagnostic already active for client")
	ErrOwnerCapacity = errors.New("diagnostic owner capacity reached")
	ErrTerminal      = errors.New("diagnostic run is terminal")
	ErrClosed        = errors.New("diagnostic service is closed")

	errCanceledByOwner = errors.New("diagnostic canceled by owner")
	errServiceClosed   = errors.New("diagnostic service closed")
)

// ClientDialer can reach only a selected owner's Tailcat client on an explicit
// Tailcat service port. Service always passes ReservedPort.
type ClientDialer interface {
	DialPort(context.Context, string, string, uint16) (net.Conn, error)
}

type AuditRecorder interface {
	Record(context.Context, audit.Entry) error
}

type EventPublisher interface {
	PublishDiagnostic(string, string, events.RuntimePhase, EventPayload)
}

type StartInput struct {
	Kind     RunKind
	Duration time.Duration
	Bytes    int64
}

type RunView struct {
	ID            string     `json:"id"`
	ClientID      string     `json:"client_id"`
	Kind          RunKind    `json:"kind"`
	Status        RunStatus  `json:"status"`
	Path          string     `json:"path,omitempty"`
	LatencyMS     *int64     `json:"latency_ms,omitempty"`
	UploadBytes   int64      `json:"upload_bytes"`
	DownloadBytes int64      `json:"download_bytes"`
	UploadBPS     int64      `json:"upload_bps"`
	DownloadBPS   int64      `json:"download_bps"`
	ErrorCode     ErrorCode  `json:"error_code,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

type EventPayload struct {
	ClientID      string    `json:"client_id"`
	Kind          RunKind   `json:"kind"`
	Status        RunStatus `json:"status"`
	Progress      int       `json:"progress"`
	LatencyMS     *int64    `json:"latency_ms,omitempty"`
	UploadBytes   int64     `json:"upload_bytes,omitzero"`
	DownloadBytes int64     `json:"download_bytes,omitzero"`
	UploadBPS     int64     `json:"upload_bps,omitzero"`
	DownloadBPS   int64     `json:"download_bps,omitzero"`
	ErrorCode     ErrorCode `json:"error_code,omitempty"`
}

type activeRun struct {
	ownerID  string
	clientID string
	kind     RunKind
	ctx      context.Context
	cancel   context.CancelCauseFunc
}

type Service struct {
	db        *ent.Client
	dialer    ClientDialer
	auditor   AuditRecorder
	publisher EventPublisher
	logger    *slog.Logger

	mu          sync.Mutex
	pendingCond *sync.Cond
	closed      bool
	pending     int
	active      map[string]*activeRun
	clientRuns  map[string]string
	ownerRuns   map[string]int
	failures    []error
	wg          sync.WaitGroup
}

func NewService(ctx context.Context, db *ent.Client, dialer ClientDialer, auditor AuditRecorder, publisher EventPublisher, logger *slog.Logger) (*Service, error) {
	if db == nil || dialer == nil || auditor == nil || publisher == nil || logger == nil {
		return nil, errors.New("diagnostic service: nil dependency")
	}
	service := &Service{
		db: db, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger,
		active: make(map[string]*activeRun), clientRuns: make(map[string]string), ownerRuns: make(map[string]int),
	}
	service.pendingCond = sync.NewCond(&service.mu)
	if err := service.reconcileAudits(ctx); err != nil {
		return nil, err
	}
	if err := service.recover(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

// reconcileAudits repairs missing lifecycle audit entries from the durable
// summary before the service admits new work. Diagnostic rows contain every
// non-sensitive field needed to reconstruct deterministic, idempotent audit
// entries, so no separate outbox schema is required.
func (s *Service) reconcileAudits(ctx context.Context) error {
	cursor := ""
	for {
		query := s.db.DiagnosticRun.Query().Order(ent.Asc(diagnosticrun.FieldID)).Limit(auditReconcilePageSize)
		if cursor != "" {
			query.Where(diagnosticrun.IDGT(cursor))
		}
		rows, err := query.All(ctx)
		if err != nil {
			return fmt.Errorf("list diagnostic audit reconciliation page: %w", err)
		}
		for _, row := range rows {
			if err := s.recordLifecycle(ctx, row.UserID, row.ClientID, row.ID, RunStatusRunning); err != nil {
				return fmt.Errorf("reconcile diagnostic start audit for run %s: %w", row.ID, err)
			}
			if row.Status != diagnosticrun.StatusRunning {
				if err := s.recordLifecycle(ctx, row.UserID, row.ClientID, row.ID, RunStatus(row.Status)); err != nil {
					return fmt.Errorf("reconcile diagnostic terminal audit for run %s: %w", row.ID, err)
				}
			}
		}
		if len(rows) < auditReconcilePageSize {
			return nil
		}
		cursor = rows[len(rows)-1].ID
	}
}

func (s *Service) List(ctx context.Context, ownerID string) ([]RunView, error) {
	if err := s.prune(ctx, ownerID); err != nil {
		return nil, err
	}
	rows, err := s.db.DiagnosticRun.Query().Where(
		diagnosticrun.UserIDEQ(ownerID),
		diagnosticrun.StartedAtGTE(time.Now().Add(-retentionAge)),
	).Order(ent.Desc(diagnosticrun.FieldStartedAt)).Limit(retainedRunsPerOwner).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list diagnostic runs: %w", err)
	}
	views := make([]RunView, 0, len(rows))
	for _, row := range rows {
		views = append(views, runView(row))
	}
	return views, nil
}

func (s *Service) Start(ctx context.Context, ownerID, clientID string, input StartInput) (RunView, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return RunView{}, ErrClosed
	}
	s.pending++
	s.mu.Unlock()
	pending := true
	defer func() {
		if pending {
			s.leavePending()
		}
	}()

	client, err := s.db.TailClient.Query().Where(tailclient.IDEQ(clientID), tailclient.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return RunView{}, ErrNotFound
	}
	if err != nil {
		return RunView{}, fmt.Errorf("validate diagnostic client ownership: %w", err)
	}
	if err := validateStartInput(input); err != nil {
		return RunView{}, err
	}

	runID := uuid.NewV7().String()
	runCtx, cancel := context.WithCancelCause(context.Background())
	active := &activeRun{ownerID: ownerID, clientID: clientID, kind: input.Kind, ctx: runCtx, cancel: cancel}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel(errServiceClosed)
		return RunView{}, ErrClosed
	}
	if s.clientRuns[clientID] != "" {
		s.mu.Unlock()
		cancel(errCanceledByOwner)
		return RunView{}, ErrClientActive
	}
	if s.ownerRuns[ownerID] >= maxActiveRunsPerOwner {
		s.mu.Unlock()
		cancel(errCanceledByOwner)
		return RunView{}, ErrOwnerCapacity
	}
	s.active[runID] = active
	s.clientRuns[clientID] = runID
	s.ownerRuns[ownerID]++
	s.mu.Unlock()

	create := s.db.DiagnosticRun.Create().
		SetID(runID).
		SetUserID(ownerID).
		SetClientID(clientID).
		SetKind(diagnosticrun.Kind(input.Kind)).
		SetStatus(diagnosticrun.StatusRunning).
		SetStartedAt(time.Now())
	if path, ok := diagnosticPath(client.LastPath); ok {
		create.SetPath(path)
	}
	row, err := create.Save(ctx)
	if err != nil {
		cancel(errCanceledByOwner)
		s.release(runID)
		return RunView{}, fmt.Errorf("create diagnostic run: %w", err)
	}
	view := runView(row)
	if err := s.prune(ctx, ownerID); err != nil {
		s.logger.ErrorContext(ctx, "Prune diagnostic history failed", "owner_id", ownerID, "error", err)
	}
	if err := s.recordLifecycle(ctx, ownerID, clientID, runID, RunStatusRunning); err != nil {
		failure := fmt.Errorf("record diagnostic start lifecycle: %w", err)
		s.recordFailure(failure)
		cancel(errCanceledByOwner)
		s.release(runID)
		return RunView{}, failure
	}
	s.publisher.PublishDiagnostic(ownerID, runID, events.RuntimePhaseRunning, eventPayload(view, 0))

	s.mu.Lock()
	s.wg.Go(func() { s.execute(runCtx, runID, input) })
	pending = false
	s.pending--
	s.pendingCond.Broadcast()
	s.mu.Unlock()
	return view, nil
}

func (s *Service) Cancel(ctx context.Context, ownerID, runID string) error {
	row, err := s.db.DiagnosticRun.Query().Where(diagnosticrun.IDEQ(runID), diagnosticrun.UserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load diagnostic run: %w", err)
	}
	if row.Status != diagnosticrun.StatusRunning {
		return ErrTerminal
	}
	s.mu.Lock()
	active := s.active[runID]
	s.mu.Unlock()
	if active == nil || active.ownerID != ownerID {
		return ErrTerminal
	}
	active.cancel(errCanceledByOwner)
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for _, run := range s.active {
			run.cancel(errServiceClosed)
		}
	}
	for s.pending > 0 {
		s.pendingCond.Wait()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	failure := errors.Join(s.failures...)
	s.mu.Unlock()
	return failure
}

func (s *Service) execute(ctx context.Context, runID string, input StartInput) {
	defer s.release(runID)
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	started := time.Now()
	progressWG.Go(func() {
		ticks := time.Tick(time.Second)
		for {
			select {
			case <-ticks:
				s.publishProgress(runID, min(99, int(time.Since(started)*100/input.Duration)))
			case <-progressDone:
				return
			}
		}
	})
	runner, err := NewRunner(func(dialCtx context.Context) (net.Conn, error) {
		s.mu.Lock()
		active := s.active[runID]
		s.mu.Unlock()
		if active == nil {
			return nil, ErrTerminal
		}
		return s.dialer.DialPort(dialCtx, active.ownerID, active.clientID, ReservedPort)
	})
	if err != nil {
		close(progressDone)
		progressWG.Wait()
		s.finish(runID, Result{}, err)
		return
	}
	result, runErr := runner.Run(ctx, Request{Kind: input.Kind, Duration: input.Duration, Bytes: input.Bytes})
	close(progressDone)
	progressWG.Wait()
	s.finish(runID, result, runErr)
}

func (s *Service) publishProgress(runID string, progress int) {
	s.mu.Lock()
	active := s.active[runID]
	if active != nil {
		ownerID, clientID, kind := active.ownerID, active.clientID, active.kind
		s.mu.Unlock()
		s.publisher.PublishDiagnostic(ownerID, runID, events.RuntimePhaseRunning, EventPayload{
			ClientID: clientID,
			Kind:     kind,
			Status:   RunStatusRunning,
			Progress: progress,
		})
		return
	}
	s.mu.Unlock()
}

func (s *Service) finish(runID string, result Result, runErr error) {
	s.mu.Lock()
	active := s.active[runID]
	s.mu.Unlock()
	if active == nil {
		return
	}
	status, phase, code := terminalOutcome(context.Cause(active.ctx), runErr)
	finishedAt := time.Now()
	terminalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var updated int
	err := retryLifecycle(terminalCtx, "persist diagnostic terminal state", func() error {
		update := s.db.DiagnosticRun.Update().Where(
			diagnosticrun.IDEQ(runID),
			diagnosticrun.UserIDEQ(active.ownerID),
			diagnosticrun.StatusEQ(diagnosticrun.StatusRunning),
		).SetStatus(diagnosticrun.Status(status)).SetFinishedAt(finishedAt)
		if result.Kind == RunKindPing && runErr == nil {
			update.SetLatencyMs(result.Latency.Milliseconds())
		}
		if result.Kind == RunKindThroughput && runErr == nil {
			update.SetUploadBytes(result.UploadBytes).
				SetDownloadBytes(result.DownloadBytes).
				SetUploadBps(bitsPerSecond(result.UploadBytes, result.Duration)).
				SetDownloadBps(bitsPerSecond(result.DownloadBytes, result.Duration))
		}
		if code != "" {
			update.SetErrorCode(code)
		}
		var saveErr error
		updated, saveErr = update.Save(terminalCtx)
		return saveErr
	})
	if err != nil {
		failure := fmt.Errorf("finalize diagnostic run %s: %w", runID, err)
		s.recordFailure(failure)
		s.logger.ErrorContext(terminalCtx, "Persist diagnostic terminal state failed", "run_id", runID, "client_id", active.clientID, "error", err)
		return
	}
	if updated != 1 {
		return
	}
	// The terminal row is now observable. Free both admission reservations
	// before audit/event/pruning work so a caller can immediately start again.
	s.release(runID)
	if err := s.recordLifecycle(terminalCtx, active.ownerID, active.clientID, runID, status); err != nil {
		failure := fmt.Errorf("record diagnostic terminal lifecycle for run %s: %w", runID, err)
		s.recordFailure(failure)
		return
	}
	s.publisher.PublishDiagnostic(active.ownerID, runID, phase, terminalEventPayload(active, status, code, result, runErr))
	if err := s.prune(terminalCtx, active.ownerID); err != nil {
		s.logger.ErrorContext(terminalCtx, "Prune diagnostic history failed", "owner_id", active.ownerID, "error", err)
	}
}

func (s *Service) recover(ctx context.Context) error {
	rows, err := s.db.DiagnosticRun.Query().Where(diagnosticrun.StatusEQ(diagnosticrun.StatusRunning)).All(ctx)
	if err != nil {
		return fmt.Errorf("list stale diagnostic runs: %w", err)
	}
	finishedAt := time.Now()
	owners := make(map[string]struct{})
	for _, row := range rows {
		// The running row is the durable recovery marker. Establish its
		// idempotent audit record first so an audit failure leaves the marker
		// available for the next startup attempt.
		if err := s.recordLifecycle(ctx, row.UserID, row.ClientID, row.ID, RunStatusInterrupted); err != nil {
			return fmt.Errorf("record recovered diagnostic lifecycle for run %s: %w", row.ID, err)
		}
		var updated int
		err := retryLifecycle(ctx, "persist recovered diagnostic state", func() error {
			update := s.db.DiagnosticRun.Update().Where(
				diagnosticrun.IDEQ(row.ID),
				diagnosticrun.UserIDEQ(row.UserID),
				diagnosticrun.StatusEQ(diagnosticrun.StatusRunning),
			).SetStatus(diagnosticrun.StatusInterrupted).
				SetErrorCode(diagnosticrun.ErrorCodeDiagnosticIo).
				SetFinishedAt(finishedAt)
			var saveErr error
			updated, saveErr = update.Save(ctx)
			return saveErr
		})
		if err != nil {
			return fmt.Errorf("interrupt stale diagnostic run: %w", err)
		}
		if updated != 1 {
			continue
		}
		row.Status = diagnosticrun.StatusInterrupted
		row.ErrorCode = diagnosticrun.ErrorCodeDiagnosticIo
		row.FinishedAt = new(finishedAt)
		view := runView(row)
		s.publisher.PublishDiagnostic(row.UserID, row.ID, events.RuntimePhaseInterrupted, eventPayload(view, 100))
		owners[row.UserID] = struct{}{}
	}
	for ownerID := range owners {
		if err := s.prune(ctx, ownerID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) prune(ctx context.Context, ownerID string) error {
	if _, err := s.db.DiagnosticRun.Delete().Where(
		diagnosticrun.UserIDEQ(ownerID),
		diagnosticrun.StartedAtLT(time.Now().Add(-retentionAge)),
	).Exec(ctx); err != nil {
		return fmt.Errorf("prune diagnostic runs by age: %w", err)
	}
	ids, err := s.db.DiagnosticRun.Query().Where(diagnosticrun.UserIDEQ(ownerID)).
		Order(ent.Desc(diagnosticrun.FieldStartedAt)).Offset(retainedRunsPerOwner).IDs(ctx)
	if err != nil {
		return fmt.Errorf("list excess diagnostic runs: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.db.DiagnosticRun.Delete().Where(diagnosticrun.UserIDEQ(ownerID), diagnosticrun.IDIn(ids...)).Exec(ctx); err != nil {
		return fmt.Errorf("prune diagnostic runs by count: %w", err)
	}
	return nil
}

func (s *Service) leavePending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending--
	s.pendingCond.Broadcast()
}

func (s *Service) release(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.active[runID]; run != nil {
		delete(s.active, runID)
		delete(s.clientRuns, run.clientID)
		s.ownerRuns[run.ownerID]--
		if s.ownerRuns[run.ownerID] == 0 {
			delete(s.ownerRuns, run.ownerID)
		}
	}
}

func (s *Service) recordLifecycle(ctx context.Context, ownerID, clientID, runID string, status RunStatus) error {
	action := "diagnostic." + string(status)
	if status == RunStatusRunning {
		action = "diagnostic.start"
	}
	entry := audit.Entry{ID: runID + ":" + action, UserID: ownerID, Action: action, ResourceKind: "diagnostic", ResourceID: runID, Outcome: "success", Detail: "client_id=" + clientID}
	if status == RunStatusFailed || status == RunStatusInterrupted {
		entry.Outcome = "failure"
	}
	return retryLifecycle(ctx, "record diagnostic audit", func() error { return s.auditor.Record(ctx, entry) })
}

func retryLifecycle(ctx context.Context, operation string, run func() error) error {
	var lastErr error
	for attempt := range lifecycleRetryAttempts {
		if err := run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == lifecycleRetryAttempts-1 {
			break
		}
		timer := time.NewTimer(lifecycleRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("%s: %w", operation, errors.Join(lastErr, ctx.Err()))
		}
	}
	return fmt.Errorf("%s after %d attempts: %w", operation, lifecycleRetryAttempts, lastErr)
}

func (s *Service) recordFailure(err error) {
	s.mu.Lock()
	s.failures = append(s.failures, err)
	s.mu.Unlock()
}

func validateStartInput(input StartInput) error {
	if input.Kind != RunKindPing && input.Kind != RunKindThroughput {
		return ErrInvalidKind
	}
	if input.Duration < time.Millisecond || input.Duration > MaxDuration {
		return ErrInvalidLimits
	}
	if input.Kind == RunKindPing && input.Bytes != 0 {
		return ErrInvalidLimits
	}
	if input.Kind == RunKindThroughput && (input.Bytes <= 0 || input.Bytes > MaxBytesPerDirection) {
		return ErrInvalidLimits
	}
	return nil
}

func terminalOutcome(cause, runErr error) (RunStatus, events.RuntimePhase, diagnosticrun.ErrorCode) {
	if errors.Is(cause, errServiceClosed) {
		return RunStatusInterrupted, events.RuntimePhaseInterrupted, diagnosticrun.ErrorCodeDiagnosticIo
	}
	if errors.Is(cause, errCanceledByOwner) {
		return RunStatusCanceled, events.RuntimePhaseStopped, diagnosticrun.ErrorCodeDiagnosticCanceled
	}
	if runErr == nil {
		return RunStatusSucceeded, events.RuntimePhaseReady, ""
	}
	code := diagnosticrun.ErrorCodeDiagnosticIo
	if protocolErr, ok := errors.AsType[*ProtocolError](runErr); ok {
		code = persistedErrorCode(protocolErr.Code)
	}
	return RunStatusFailed, events.RuntimePhaseError, code
}

func persistedErrorCode(code ErrorCode) diagnosticrun.ErrorCode {
	switch code {
	case CodeCanceled:
		return diagnosticrun.ErrorCodeDiagnosticCanceled
	case CodeTimeout:
		return diagnosticrun.ErrorCodeDiagnosticTimeout
	case CodeInvalidMagic:
		return diagnosticrun.ErrorCodeDiagnosticInvalidMagic
	case CodeHeaderTooLarge:
		return diagnosticrun.ErrorCodeDiagnosticHeaderTooLarge
	case CodeMalformedHeader:
		return diagnosticrun.ErrorCodeDiagnosticMalformedHeader
	case CodeInvalidRequest:
		return diagnosticrun.ErrorCodeDiagnosticInvalidRequest
	case CodeLimitExceeded:
		return diagnosticrun.ErrorCodeDiagnosticLimitExceeded
	case CodeInvalidRunner:
		return diagnosticrun.ErrorCodeDiagnosticInvalidRunner
	case CodeIO:
		return diagnosticrun.ErrorCodeDiagnosticIo
	default:
		return diagnosticrun.ErrorCodeDiagnosticIo
	}
}

func diagnosticPath(path string) (diagnosticrun.Path, bool) {
	switch path {
	case "direct":
		return diagnosticrun.PathDirect, true
	case "derp":
		return diagnosticrun.PathDerp, true
	case "peer-relay", "peer_relay":
		return diagnosticrun.PathPeerRelay, true
	default:
		return "", false
	}
}

func bitsPerSecond(bytes int64, duration time.Duration) int64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bytes*8) / duration.Seconds())
}

func runView(row *ent.DiagnosticRun) RunView {
	return RunView{
		ID: row.ID, ClientID: row.ClientID, Kind: RunKind(row.Kind), Status: RunStatus(row.Status), Path: string(row.Path),
		LatencyMS: row.LatencyMs, UploadBytes: row.UploadBytes, DownloadBytes: row.DownloadBytes,
		UploadBPS: row.UploadBps, DownloadBPS: row.DownloadBps, ErrorCode: ErrorCode(row.ErrorCode),
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
}

func eventPayload(view RunView, progress int) EventPayload {
	return EventPayload{
		ClientID: view.ClientID, Kind: view.Kind, Status: view.Status, Progress: progress,
		LatencyMS: view.LatencyMS, UploadBytes: view.UploadBytes, DownloadBytes: view.DownloadBytes,
		UploadBPS: view.UploadBPS, DownloadBPS: view.DownloadBPS, ErrorCode: view.ErrorCode,
	}
}

func terminalEventPayload(active *activeRun, status RunStatus, code diagnosticrun.ErrorCode, result Result, runErr error) EventPayload {
	payload := EventPayload{
		ClientID:  active.clientID,
		Kind:      active.kind,
		Status:    status,
		Progress:  100,
		ErrorCode: ErrorCode(code),
	}
	if result.Kind == RunKindPing && runErr == nil {
		payload.LatencyMS = new(result.Latency.Milliseconds())
	}
	if result.Kind == RunKindThroughput && runErr == nil {
		payload.UploadBytes = result.UploadBytes
		payload.DownloadBytes = result.DownloadBytes
		payload.UploadBPS = bitsPerSecond(result.UploadBytes, result.Duration)
		payload.DownloadBPS = bitsPerSecond(result.DownloadBytes, result.Duration)
	}
	return payload
}
