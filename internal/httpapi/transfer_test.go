package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/auditevent"
	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/events"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/transfer"

	_ "github.com/lib-x/entsqlite"
	"golang.org/x/time/rate"
)

type transferAPIPublisher struct{}

var transferAPITestSequence atomic.Uint64

func (transferAPIPublisher) PublishEvent(string, events.Envelope) {}

type transferAPIDialer struct {
	service  *transfer.Service
	serverID string
	calls    atomic.Int64
	block    atomic.Pointer[chan struct{}]
}

func (dialer *transferAPIDialer) DialPort(ctx context.Context, _, _ string, port uint16) (net.Conn, error) {
	if port != transfer.ReservedPort {
		return nil, net.ErrClosed
	}
	call := dialer.calls.Add(1)
	if blocker := dialer.block.Load(); blocker != nil && call > 1 {
		select {
		case <-*blocker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	client, server := net.Pipe()
	go dialer.service.ReservedHandler(dialer.serverID)(ctx, server)
	return client, nil
}

type transferAPIHarness struct {
	t       *testing.T
	db      *ent.Client
	service *transfer.Service
	api     *API
	handler http.Handler
	ownerA  *ent.User
	ownerB  *ent.User
	tokenA  string
	tokenB  string
	serverA *ent.TailServer
	clientA *ent.TailClient
	dialer  *transferAPIDialer
}

func newTransferAPIHarness(t *testing.T, limits config.Transfer) *transferAPIHarness {
	t.Helper()
	if limits == (config.Transfer{}) {
		limits = config.DefaultTransfer()
	}
	sequence := strconv.FormatUint(transferAPITestSequence.Add(1), 10)
	db := enttest.Open(t, "sqlite3", "file:"+url.QueryEscape(t.Name()+"-"+sequence)+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ownerA := db.User.Create().SetIssuer("test").SetSubject(t.Name() + "-" + sequence + "-owner-a").SaveX(t.Context())
	ownerB := db.User.Create().SetIssuer("test").SetSubject(t.Name() + "-" + sequence + "-owner-b").SaveX(t.Context())
	serverA := db.TailServer.Create().SetUserID(ownerA.ID).SetName("sender").SaveX(t.Context())
	clientA := db.TailClient.Create().SetUserID(ownerA.ID).SetName("receiver").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
	storageRoot := t.TempDir()
	if err := os.Chmod(storageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := transfer.NewStorageWithLimits(storageRoot, transfer.StorageLimits{
		MaxFileBytes: limits.MaxFileBytes, MaxScopeBytes: max(limits.MaxShareBytes, limits.MaxJobBytes),
		MaxOwnerBytes: limits.MaxOwnerBytes, MaxFilesPerScope: limits.MaxFilesPerShare,
	})
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	auditor, err := audit.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	dialer := new(transferAPIDialer)
	service, err := transfer.NewServiceWithLimits(t.Context(), db, storage, box, dialer, auditor, transferAPIPublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)), transfer.ServiceLimits{
		MaxFileBytes: limits.MaxFileBytes, MaxShareBytes: limits.MaxShareBytes, MaxJobBytes: limits.MaxJobBytes,
		MaxFilesPerShare: limits.MaxFilesPerShare, Workers: limits.Workers,
		MaxJobsPerOwner: limits.MaxJobsPerOwner, Expiry: limits.Expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer.service = service
	dialer.serverID = serverA.ID
	t.Cleanup(func() {
		_ = service.Close()
		_ = storage.Close()
	})
	baseURL, _ := url.Parse("https://tailcat.example.com")
	publishURL, _ := url.Parse("https://publish.tailcat.example.com")
	cfg := config.Config{
		BaseURL: baseURL, PublishURL: publishURL, DemoMode: true, DemoEmail: "operator@example.test",
		SessionIdle: 24 * time.Hour, SessionMax: 7 * 24 * time.Hour, Transfer: limits,
	}
	authService, err := auth.NewService(t.Context(), db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	tokenA := transferAPISession(t, db, ownerA.ID, "owner-a-token")
	tokenB := transferAPISession(t, db, ownerB.ID, "owner-b-token")
	api := &API{
		db: db, auth: authService, audit: auditor, transfer: service, cfg: cfg,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), web: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}},
		startedAt: time.Now(), tunnels: make(map[string]int), mutationRates: make(map[string]*rate.Limiter),
		mutationActive: make(map[string]int), mutationSlots: make(chan struct{}, 64), eventStreams: make(map[string]int),
	}
	handler, err := api.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return &transferAPIHarness{t: t, db: db, service: service, api: api, handler: handler, ownerA: ownerA, ownerB: ownerB, tokenA: tokenA, tokenB: tokenB, serverA: serverA, clientA: clientA, dialer: dialer}
}

func transferAPISession(t *testing.T, db *ent.Client, ownerID, token string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	db.Session.Create().SetUserID(ownerID).SetTokenHash(hex.EncodeToString(hash[:])).SetLastSeenAt(now).SetExpiresAt(now.Add(time.Hour)).SaveX(t.Context())
	return token
}

func (h *transferAPIHarness) request(method, path, token, contentType string, body io.Reader) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, "https://tailcat.example.com"+path, body)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func (h *transferAPIHarness) createShare(token string) (string, string) {
	h.t.Helper()
	response := h.request(http.MethodPost, "/api/v1/transfers/shares", token, "application/json", strings.NewReader(`{"server_id":"`+h.serverA.ID+`"}`))
	if response.Code != http.StatusCreated {
		h.t.Fatalf("create share = %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Share struct {
			ID string `json:"id"`
		} `json:"share"`
		Capability string `json:"capability"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		h.t.Fatal(err)
	}
	if created.Share.ID == "" || created.Capability == "" {
		h.t.Fatalf("create share response = %s", response.Body.String())
	}
	return created.Share.ID, created.Capability
}

func (h *transferAPIHarness) upload(token, shareID, virtualPath string, payload []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Tailcat-Virtual-Path", virtualPath)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func TestTransferShareAPIUsesStrictOneTimeCapabilityDTOsAndOwnerHiding(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	unknown := h.request(http.MethodPost, "/api/v1/transfers/shares", h.tokenA, "application/json", strings.NewReader(`{"server_id":"`+h.serverA.ID+`","capability":"forbidden"}`))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown create field = %d %s", unknown.Code, unknown.Body.String())
	}

	shareID, capability := h.createShare(h.tokenA)
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares", h.tokenB, "application/json", strings.NewReader(`{"server_id":"`+h.serverA.ID+`"}`)); response.Code != http.StatusNotFound {
		t.Fatalf("foreign server share create = %d %s", response.Code, response.Body.String())
	}
	for _, endpoint := range []string{
		"/api/v1/transfers/shares",
		"/api/v1/transfers/shares/" + shareID,
		"/api/v1/transfers/shares/" + shareID + "/files",
	} {
		response := h.request(http.MethodGet, endpoint, h.tokenA, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", endpoint, response.Code, response.Body.String())
		}
		for _, forbidden := range []string{capability, "capability_hash", "remote_capability_cipher", "storage_name", "blake3", "block_hashes"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("GET %s leaked %q: %s", endpoint, forbidden, response.Body.String())
			}
		}
	}
	if response := h.request(http.MethodDelete, "/api/v1/transfers/shares/"+shareID, h.tokenB, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("foreign DELETE share = %d %s", response.Code, response.Body.String())
	}

	rotated := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/rotate", h.tokenA, "", nil)
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"capability":"tcs1.`) || strings.Contains(rotated.Body.String(), capability) {
		t.Fatalf("rotate response = %d %s", rotated.Code, rotated.Body.String())
	}
	for _, endpoint := range []string{
		"/api/v1/transfers/shares/" + shareID,
		"/api/v1/transfers/shares/" + shareID + "/files",
		"/api/v1/transfers/shares/" + shareID + "/finalize",
		"/api/v1/transfers/shares/" + shareID + "/rotate",
	} {
		method := http.MethodGet
		if strings.HasSuffix(endpoint, "/finalize") || strings.HasSuffix(endpoint, "/rotate") {
			method = http.MethodPost
		}
		response := h.request(method, endpoint, h.tokenB, "", nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s %s = %d %s", method, endpoint, response.Code, response.Body.String())
		}
	}
	if response := h.request(http.MethodDelete, "/api/v1/transfers/shares/"+shareID, h.tokenA, "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("owner DELETE share = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodGet, "/api/v1/transfers/shares/"+shareID, h.tokenA, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("get deleted share = %d %s", response.Code, response.Body.String())
	}
}

type observedBody struct {
	reads  int
	closed bool
	data   *bytes.Reader
	mu     sync.Mutex
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	body.mu.Lock()
	body.reads++
	body.mu.Unlock()
	return body.data.Read(buffer)
}

func (body *observedBody) Close() error {
	body.mu.Lock()
	body.closed = true
	body.mu.Unlock()
	return nil
}

func TestTransferUploadAuthenticatesAndChecksOwnerBeforeReading(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, _ := h.createShare(h.tokenA)
	for name, token := range map[string]string{"unauthenticated": "", "foreign": h.tokenB} {
		t.Run(name, func(t *testing.T) {
			body := &observedBody{data: bytes.NewReader([]byte("secret"))}
			request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", body)
			request.ContentLength = 6
			request.Header.Set("Content-Type", "application/octet-stream")
			request.Header.Set("X-Tailcat-Virtual-Path", "secret.txt")
			if token != "" {
				request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			}
			response := httptest.NewRecorder()
			h.handler.ServeHTTP(response, request)
			if want := http.StatusUnauthorized; name == "foreign" {
				want = http.StatusNotFound
				if response.Code != want {
					t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
				}
			} else if response.Code != want {
				t.Fatalf("status = %d, want %d: %s", response.Code, want, response.Body.String())
			}
			body.mu.Lock()
			defer body.mu.Unlock()
			if body.reads != 0 {
				t.Fatalf("body read %d times before auth/owner eligibility", body.reads)
			}
			if name == "foreign" && !body.closed {
				t.Fatal("foreign ineligible upload body was not closed")
			}
		})
	}
}

func TestTransferUploadValidatesRawLengthPathSizeMismatchQuotaAndFinalize(t *testing.T) {
	limits := config.DefaultTransfer()
	limits.MaxFileBytes = 4
	limits.MaxShareBytes = 4
	limits.MaxJobBytes = 4
	limits.MaxOwnerBytes = 4
	limits.MaxFilesPerShare = 2
	h := newTransferAPIHarness(t, limits)
	shareID, _ := h.createShare(h.tokenA)

	wrongType := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/files", h.tokenA, "text/plain", strings.NewReader("a"))
	if wrongType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type = %d %s", wrongType.Code, wrongType.Body.String())
	}
	missingLengthRequest := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", io.NopCloser(strings.NewReader("a")))
	missingLengthRequest.ContentLength = -1
	missingLengthRequest.Header.Set("Content-Type", "application/octet-stream")
	missingLengthRequest.Header.Set("X-Tailcat-Virtual-Path", "a.txt")
	missingLengthRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	missingLength := httptest.NewRecorder()
	h.handler.ServeHTTP(missingLength, missingLengthRequest)
	if missingLength.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing length = %d %s", missingLength.Code, missingLength.Body.String())
	}
	for _, virtualPath := range []string{"../escape", "/absolute", "a//b", strings.Repeat("a", 1025), "bad\r\nname.txt"} {
		response := h.upload(h.tokenA, shareID, virtualPath, []byte("a"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("path %q = %d %s", virtualPath, response.Code, response.Body.String())
		}
	}
	tooLarge := h.upload(h.tokenA, shareID, "large.bin", []byte("12345"))
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload = %d %s", tooLarge.Code, tooLarge.Body.String())
	}
	maxBytesRequest := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", strings.NewReader("12345"))
	maxBytesRequest.ContentLength = 4
	maxBytesRequest.Header.Set("Content-Type", "application/octet-stream")
	maxBytesRequest.Header.Set("X-Tailcat-Virtual-Path", "bounded.bin")
	maxBytesRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	maxBytes := httptest.NewRecorder()
	h.handler.ServeHTTP(maxBytes, maxBytesRequest)
	if maxBytes.Code != http.StatusRequestEntityTooLarge || !strings.Contains(maxBytes.Body.String(), `"code":"TRANSFER_FILE_TOO_LARGE"`) {
		t.Fatalf("MaxBytesReader overflow = %d %s", maxBytes.Code, maxBytes.Body.String())
	}
	mismatchRequest := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", strings.NewReader("abc"))
	mismatchRequest.ContentLength = 2
	mismatchRequest.Header.Set("Content-Type", "application/octet-stream")
	mismatchRequest.Header.Set("X-Tailcat-Virtual-Path", "mismatch.txt")
	mismatchRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	mismatch := httptest.NewRecorder()
	h.handler.ServeHTTP(mismatch, mismatchRequest)
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("actual mismatch = %d %s", mismatch.Code, mismatch.Body.String())
	}
	zero := h.upload(h.tokenA, shareID, "empty.txt", nil)
	if zero.Code != http.StatusCreated {
		t.Fatalf("zero upload = %d %s", zero.Code, zero.Body.String())
	}
	full := h.upload(h.tokenA, shareID, "full.bin", []byte("1234"))
	if full.Code != http.StatusCreated {
		t.Fatalf("full upload = %d %s", full.Code, full.Body.String())
	}
	quota := h.upload(h.tokenA, shareID, "quota.bin", nil)
	if quota.Code != http.StatusConflict {
		t.Fatalf("file-count quota = %d %s", quota.Code, quota.Body.String())
	}
	otherShareID, _ := h.createShare(h.tokenA)
	if response := h.upload(h.tokenA, otherShareID, "owner-quota.bin", []byte("x")); response.Code != http.StatusConflict {
		t.Fatalf("owner byte quota = %d %s", response.Code, response.Body.String())
	}
	finalized := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil)
	if finalized.Code != http.StatusOK || !strings.Contains(finalized.Body.String(), `"status":"ready"`) {
		t.Fatalf("finalize = %d %s", finalized.Code, finalized.Body.String())
	}
	if response := h.upload(h.tokenA, shareID, "late.txt", nil); response.Code != http.StatusConflict {
		t.Fatalf("upload after finalize = %d %s", response.Code, response.Body.String())
	}
	if count := h.db.AuditEvent.Query().Where(auditevent.UserIDEQ(h.ownerA.ID), auditevent.ActionEQ("POST /api/v1/transfers/shares/:id/files"), auditevent.OutcomeEQ(auditevent.OutcomeSuccess)).CountX(t.Context()); count < 2 {
		t.Fatalf("successful upload mutation audits = %d, want at least 2", count)
	}
}

type blockingUploadBody struct {
	readStarted chan struct{}
	closed      chan struct{}
	once        sync.Once
}

func (body *blockingUploadBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.readStarted) })
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *blockingUploadBody) Close() error {
	select {
	case <-body.closed:
	default:
		close(body.closed)
	}
	return nil
}

func TestTransferUploadCancellationClosesBodyAndLeavesNoFile(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, _ := h.createShare(h.tokenA)
	body := &blockingUploadBody{readStarted: make(chan struct{}), closed: make(chan struct{})}
	requestContext, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", body).WithContext(requestContext)
	request.ContentLength = 1
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Tailcat-Virtual-Path", "cancel.bin")
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	finished := make(chan struct{})
	go func() {
		h.handler.ServeHTTP(httptest.NewRecorder(), request)
		close(finished)
	}()
	<-body.readStarted
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled upload did not return")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("canceled upload body was not closed")
	}
	files, err := h.service.ListShareFiles(t.Context(), h.ownerA.ID, shareID)
	if err != nil || len(files) != 0 {
		t.Fatalf("files after canceled upload = %+v, err=%v", files, err)
	}
}

func TestTransferReceiveWorkflowAndCompletedDownload(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, capability := h.createShare(h.tokenA)
	payload := []byte("verified download body")
	virtualPath := `folder/héllo "quote".txt`
	if response := h.upload(h.tokenA, shareID, virtualPath, payload); response.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatalf("finalize = %d %s", response.Code, response.Body.String())
	}
	unknown := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`","target":"/tmp"}`))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown job field = %d %s", unknown.Code, unknown.Body.String())
	}
	created := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create job = %d %s", created.Code, created.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenB, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`)); response.Code != http.StatusNotFound {
		t.Fatalf("foreign client job create = %d %s", response.Code, response.Body.String())
	}
	started := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/start", h.tokenA, "", nil)
	if started.Code != http.StatusOK {
		t.Fatalf("start job = %d %s", started.Code, started.Body.String())
	}
	var itemID string
	for range 100 {
		response := h.request(http.MethodGet, "/api/v1/transfers/jobs/"+job.ID+"/items", h.tokenA, "", nil)
		var items struct {
			Items []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items.Items) == 1 && items.Items[0].Status == "completed" {
			itemID = items.Items[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if itemID == "" {
		t.Fatal("job did not complete")
	}
	for _, endpoint := range []string{
		"/api/v1/transfers/jobs",
		"/api/v1/transfers/jobs/" + job.ID,
		"/api/v1/transfers/jobs/" + job.ID + "/items",
		"/api/v1/transfers/jobs/" + job.ID + "/items/" + itemID,
	} {
		response := h.request(http.MethodGet, endpoint, h.tokenA, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", endpoint, response.Code, response.Body.String())
		}
		for _, forbidden := range []string{"storage_name", "remote_capability_cipher", "blake3", "block_hashes"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("GET %s leaked %q", endpoint, forbidden)
			}
		}
	}
	downloadPath := "/api/v1/transfers/jobs/" + job.ID + "/items/" + itemID + "/download"
	download := h.request(http.MethodGet, downloadPath, h.tokenA, "", nil)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), payload) {
		t.Fatalf("download = %d %q", download.Code, download.Body.Bytes())
	}
	if got := download.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := download.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := download.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if download.Header().Get("Accept-Ranges") != "bytes" || download.Header().Get("Content-Length") != "22" || download.Header().Get("Last-Modified") == "" {
		t.Fatalf("download length/range/conditional headers = %v", download.Header())
	}
	disposition := download.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "filename*=") || strings.ContainsAny(disposition, "\r\n") {
		t.Fatalf("unsafe/non-Unicode Content-Disposition = %q", disposition)
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
	rangeRequest.Header.Set("Range", "bytes=9-16")
	rangeRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	rangeResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "download" {
		t.Fatalf("range = %d %q headers=%v", rangeResponse.Code, rangeResponse.Body.String(), rangeResponse.Header())
	}
	if rangeResponse.Header().Get("Content-Range") != "bytes 9-16/22" || rangeResponse.Header().Get("Content-Length") != "8" || rangeResponse.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range headers = %v", rangeResponse.Header())
	}
	for name, ifRange := range map[string]string{
		"matching": download.Header().Get("Last-Modified"),
		"stale":    time.Unix(1, 0).UTC().Format(http.TimeFormat),
	} {
		t.Run("if-range-"+name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
			request.Header.Set("Range", "bytes=9-")
			request.Header.Set("If-Range", ifRange)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
			response := httptest.NewRecorder()
			h.handler.ServeHTTP(response, request)
			want := http.StatusPartialContent
			if name == "stale" {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("If-Range %s = %d headers=%v", name, response.Code, response.Header())
			}
		})
	}
	malformedIfRange := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
	malformedIfRange.Header.Set("Range", "bytes=abc-def")
	malformedIfRange.Header.Set("If-Range", time.Unix(1, 0).UTC().Format(http.TimeFormat))
	malformedIfRange.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	malformedIfRangeResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(malformedIfRangeResponse, malformedIfRange)
	if malformedIfRangeResponse.Code != http.StatusRequestedRangeNotSatisfiable || malformedIfRangeResponse.Body.Len() != 0 || malformedIfRangeResponse.Header().Get("Content-Range") != "bytes */22" {
		t.Fatalf("malformed If-Range = %d headers=%v body=%q", malformedIfRangeResponse.Code, malformedIfRangeResponse.Header(), malformedIfRangeResponse.Body.String())
	}
	multipleHeaders := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
	multipleHeaders.Header.Add("Range", "bytes=0-1")
	multipleHeaders.Header.Add("Range", "bytes=3-4")
	multipleHeaders.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	multipleHeadersResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(multipleHeadersResponse, multipleHeaders)
	if multipleHeadersResponse.Code != http.StatusRequestedRangeNotSatisfiable || multipleHeadersResponse.Body.Len() != 0 {
		t.Fatalf("multiple Range headers = %d body=%q", multipleHeadersResponse.Code, multipleHeadersResponse.Body.String())
	}
	for name, rangeValue := range map[string]string{"empty": "", "empty-spec": "bytes=", "multiple": "bytes=0-1,3-4", "invalid": "bytes=nope", "unsatisfiable": "bytes=999-1000"} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
			request.Header.Set("Range", rangeValue)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
			response := httptest.NewRecorder()
			h.handler.ServeHTTP(response, request)
			if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Header().Get("Content-Range") != "bytes */22" {
				t.Fatalf("range %q = %d headers=%v body=%q", rangeValue, response.Code, response.Header(), response.Body.String())
			}
			if name == "multiple" && response.Body.Len() != 0 {
				t.Fatalf("multi-range body = %q", response.Body.String())
			}
		})
	}
	for name, header := range map[string]string{
		"not-modified":      "If-Modified-Since",
		"precondition-fail": "If-Unmodified-Since",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
			when := time.Now().UTC().Add(time.Hour)
			want := http.StatusNotModified
			if header == "If-Unmodified-Since" {
				when = time.Unix(1, 0).UTC()
				want = http.StatusPreconditionFailed
			}
			request.Header.Set(header, when.Format(http.TimeFormat))
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
			response := httptest.NewRecorder()
			h.handler.ServeHTTP(response, request)
			if response.Code != want || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "" || response.Header().Get("Last-Modified") == "" {
				t.Fatalf("%s = %d length=%q body=%q", header, response.Code, response.Header().Get("Content-Length"), response.Body.String())
			}
		})
	}
	headRequest := httptest.NewRequest(http.MethodHead, "https://tailcat.example.com"+downloadPath, nil)
	headRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	headResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(headResponse, headRequest)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != "22" {
		t.Fatalf("HEAD download = %d length=%q body=%q", headResponse.Code, headResponse.Header().Get("Content-Length"), headResponse.Body.String())
	}
	headRangeRequest := httptest.NewRequest(http.MethodHead, "https://tailcat.example.com"+downloadPath, nil)
	headRangeRequest.Header.Set("Range", "bytes=9-16")
	headRangeRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	headRangeResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(headRangeResponse, headRangeRequest)
	if headRangeResponse.Code != http.StatusPartialContent || headRangeResponse.Body.Len() != 0 || headRangeResponse.Header().Get("Content-Range") != "bytes 9-16/22" || headRangeResponse.Header().Get("Content-Length") != "8" {
		t.Fatalf("HEAD range = %d headers=%v body=%q", headRangeResponse.Code, headRangeResponse.Header(), headRangeResponse.Body.String())
	}
	if foreign := h.request(http.MethodGet, downloadPath, h.tokenB, "", nil); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign download = %d %s", foreign.Code, foreign.Body.String())
	}
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": safeDownloadName("bad\r\nInjected.txt")}); strings.ContainsAny(disposition, "\r\n") || strings.Contains(disposition, "\r") || strings.Contains(disposition, "\n") {
		t.Fatalf("CRLF download disposition = %q", disposition)
	}
	sanitizedC1 := safeDownloadName("bad\u0085Injected.txt")
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": sanitizedC1}); sanitizedC1 != "bad_Injected.txt" || strings.Contains(disposition, "%C2%85") || strings.ContainsRune(disposition, '\u0085') {
		t.Fatalf("C1 download name=%q disposition=%q", sanitizedC1, disposition)
	}
}

func TestStrictSingleDownloadRangeParser(t *testing.T) {
	for _, test := range []struct {
		header string
		size   int64
		valid  bool
	}{
		{"", 10, false},
		{"bytes=0-0", 10, true},
		{"bytes=3-", 10, true},
		{"bytes=-3", 10, true},
		{"bytes=-11", 10, true},
		{"bytes=0-99", 10, true},
		{"bytes=", 10, false},
		{"bytes=abc-def", 10, false},
		{"bytes=١-٢", 10, false},
		{"bytes=-0", 10, false},
		{"bytes=--1", 10, false},
		{"bytes=+1-2", 10, false},
		{"bytes=1--2", 10, false},
		{"bytes=3-2", 10, false},
		{"bytes=10-", 10, false},
		{"bytes=0-1,3-4", 10, false},
		{"bytes=9223372036854775808-", 10, false},
		{"bytes=-9223372036854775808", 10, false},
		{"items=0-1", 10, false},
		{"bytes=0-0", 0, false},
		{"bytes=-1", 0, false},
	} {
		if got := validSingleDownloadRange(test.header, test.size); got != test.valid {
			t.Errorf("validSingleDownloadRange(%q, %d) = %t, want %t", test.header, test.size, got, test.valid)
		}
	}
}

func TestZeroByteCompletedDownloadRejectsEveryRange(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, capability := h.createShare(h.tokenA)
	if response := h.upload(h.tokenA, shareID, "empty.txt", nil); response.Code != http.StatusCreated {
		t.Fatalf("zero upload = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	created := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`))
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/start", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	waitTransferAPIJobStatus(t, h, job.ID, "completed")
	items := h.request(http.MethodGet, "/api/v1/transfers/jobs/"+job.ID+"/items", h.tokenA, "", nil)
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(items.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("items = %s, %v", items.Body.String(), err)
	}
	downloadPath := "/api/v1/transfers/jobs/" + job.ID + "/items/" + listed.Items[0].ID + "/download"
	for _, value := range []string{"bytes=0-0", "bytes=-1", "bytes=abc-def"} {
		request := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com"+downloadPath, nil)
		request.Header.Set("Range", value)
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
		response := httptest.NewRecorder()
		h.handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.Len() != 0 || response.Header().Get("Content-Range") != "bytes */0" {
			t.Fatalf("zero-byte range %q = %d headers=%v body=%q", value, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestTransferIncompleteDownloadAndForeignJobActionsAreHidden(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, capability := h.createShare(h.tokenA)
	if response := h.upload(h.tokenA, shareID, "pending.txt", []byte("abc")); response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	created := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`))
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	items := h.request(http.MethodGet, "/api/v1/transfers/jobs/"+job.ID+"/items", h.tokenA, "", nil)
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(items.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("items = %s, err=%v", items.Body.String(), err)
	}
	downloadPath := "/api/v1/transfers/jobs/" + job.ID + "/items/" + listed.Items[0].ID + "/download"
	if response := h.request(http.MethodGet, downloadPath, h.tokenA, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("incomplete download = %d %s", response.Code, response.Body.String())
	}
	for _, action := range []string{"start", "cancel", "retry"} {
		response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/"+action, h.tokenB, "", nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s = %d %s", action, response.Code, response.Body.String())
		}
	}
	if response := h.request(http.MethodDelete, "/api/v1/transfers/jobs/"+job.ID, h.tokenB, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d %s", response.Code, response.Body.String())
	}
	for _, endpoint := range []string{
		"/api/v1/transfers/jobs/" + job.ID,
		"/api/v1/transfers/jobs/" + job.ID + "/items",
		"/api/v1/transfers/jobs/" + job.ID + "/items/" + listed.Items[0].ID,
		downloadPath,
	} {
		if response := h.request(http.MethodGet, endpoint, h.tokenB, "", nil); response.Code != http.StatusNotFound {
			t.Fatalf("foreign GET %s = %d %s", endpoint, response.Code, response.Body.String())
		}
	}
	if response := h.request(http.MethodDelete, "/api/v1/transfers/jobs/"+job.ID, h.tokenA, "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("owner delete job = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodGet, "/api/v1/transfers/jobs/"+job.ID, h.tokenA, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("get deleted job = %d %s", response.Code, response.Body.String())
	}
}

func TestTransferJobCancelAndRetryCompleteTheReceiveWorkflow(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, capability := h.createShare(h.tokenA)
	if response := h.upload(h.tokenA, shareID, "resume.txt", []byte("resume")); response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	created := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`))
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	h.dialer.block.Store(&block)
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/start", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/cancel", h.tokenA, "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", response.Code, response.Body.String())
	}
	waitTransferAPIJobStatus(t, h, job.ID, "canceled")
	close(block)
	h.dialer.block.Store(nil)
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+job.ID+"/retry", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", response.Code, response.Body.String())
	}
	waitTransferAPIJobStatus(t, h, job.ID, "completed")
}

func waitTransferAPIJobStatus(t *testing.T, h *transferAPIHarness, jobID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := h.request(http.MethodGet, "/api/v1/transfers/jobs/"+jobID, h.tokenA, "", nil)
		var job struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &job); err == nil && job.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", jobID, want)
}

func TestTransferUploadBodyLimitBypassMatchesOnlyExactRouteShape(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/v1/transfers/shares/id/files", true},
		{http.MethodGet, "/api/v1/transfers/shares/id/files", false},
		{http.MethodPost, "/api/v1/transfers/shares/id/files/extra", false},
		{http.MethodPost, "/api/v1/transfers/shares//files", false},
		{http.MethodPost, "/api/v1/transfers/jobs/id/files", false},
	} {
		request := httptest.NewRequest(test.method, "https://tailcat.example.com"+test.path, nil)
		if got := isTransferUploadRequest(request); got != test.want {
			t.Errorf("isTransferUploadRequest(%s %s) = %t, want %t", test.method, test.path, got, test.want)
		}
	}
}

func TestTransferUploadBodyLimitBypassRemainsInsideAllManagementMiddleware(t *testing.T) {
	h := newTransferAPIHarness(t, config.Transfer{})
	shareID, _ := h.createShare(h.tokenA)
	payload := bytes.Repeat([]byte("x"), (1<<20)+1)
	if response := h.upload(h.tokenA, shareID, "large-browser.bin", payload); response.Code != http.StatusCreated {
		t.Fatalf("exact upload route did not bypass global body limit: %d %s", response.Code, response.Body.String())
	}
	for name, target := range map[string]struct{ method, path string }{
		"method":     {http.MethodPut, "/api/v1/transfers/shares/" + shareID + "/files"},
		"missing-id": {http.MethodPost, "/api/v1/transfers/shares//files"},
		"sibling":    {http.MethodPost, "/api/v1/transfers/shares/" + shareID + "/finalize"},
		"deeper":     {http.MethodPost, "/api/v1/transfers/shares/" + shareID + "/files/extra"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(target.method, "https://tailcat.example.com"+target.path, bytes.NewReader(payload))
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
			response := httptest.NewRecorder()
			h.handler.ServeHTTP(response, request)
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s %s = %d %s", target.method, target.path, response.Code, response.Body.String())
			}
		})
	}
	unauthBody := &observedBody{data: bytes.NewReader([]byte("secret"))}
	unauth := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", unauthBody)
	unauth.ContentLength = int64(len(payload))
	unauth.Header.Set("Content-Type", "application/octet-stream")
	unauth.Header.Set("X-Tailcat-Virtual-Path", "unauth.bin")
	unauthResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(unauthResponse, unauth)
	if unauthResponse.Code != http.StatusUnauthorized || unauthBody.reads != 0 {
		t.Fatalf("unauthenticated exact upload = %d reads=%d", unauthResponse.Code, unauthBody.reads)
	}
	wrongOriginBody := &observedBody{data: bytes.NewReader([]byte("secret"))}
	wrongOrigin := httptest.NewRequest(http.MethodPost, "https://other.example/api/v1/transfers/shares/"+shareID+"/files", wrongOriginBody)
	wrongOrigin.ContentLength = int64(len(payload))
	wrongOrigin.Header.Set("Content-Type", "application/octet-stream")
	wrongOrigin.Header.Set("X-Tailcat-Virtual-Path", "origin.bin")
	wrongOrigin.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	wrongOriginResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(wrongOriginResponse, wrongOrigin)
	if wrongOriginResponse.Code != http.StatusNotFound || wrongOriginBody.reads != 0 {
		t.Fatalf("wrong-origin exact upload = %d reads=%d", wrongOriginResponse.Code, wrongOriginBody.reads)
	}
	h.api.mutationMu.Lock()
	h.api.mutationRates[h.ownerA.ID] = rate.NewLimiter(0, 0)
	h.api.mutationMu.Unlock()
	rateBody := &observedBody{data: bytes.NewReader(nil)}
	rateRequest := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/transfers/shares/"+shareID+"/files", rateBody)
	rateRequest.ContentLength = 0
	rateRequest.Header.Set("Content-Type", "application/octet-stream")
	rateRequest.Header.Set("X-Tailcat-Virtual-Path", "rate.bin")
	rateRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokenA})
	rateResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(rateResponse, rateRequest)
	if rateResponse.Code != http.StatusTooManyRequests || rateBody.reads != 0 {
		t.Fatalf("rate-limited exact upload = %d reads=%d", rateResponse.Code, rateBody.reads)
	}
	if count := h.db.AuditEvent.Query().Where(auditevent.UserIDEQ(h.ownerA.ID), auditevent.ActionEQ("POST /api/v1/transfers/shares/:id/files"), auditevent.OutcomeEQ(auditevent.OutcomeSuccess)).CountX(t.Context()); count != 1 {
		t.Fatalf("exact upload success audits = %d, want 1", count)
	}
}

func TestTransferPublicConfigAndConfiguredSingleJobAdmission(t *testing.T) {
	limits := config.DefaultTransfer()
	limits.MaxJobsPerOwner = 1
	limits.Workers = 4
	h := newTransferAPIHarness(t, limits)
	public := h.request(http.MethodGet, "/api/v1/config", "", "", nil)
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), `"workers":4`) || !strings.Contains(public.Body.String(), `"max_jobs_per_owner":1`) {
		t.Fatalf("public transfer config = %d %s", public.Code, public.Body.String())
	}
	shareID, capability := h.createShare(h.tokenA)
	if response := h.upload(h.tokenA, shareID, "capacity.txt", []byte("capacity")); response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/shares/"+shareID+"/finalize", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	jobIDs := make([]string, 2)
	for index := range jobIDs {
		created := h.request(http.MethodPost, "/api/v1/transfers/jobs", h.tokenA, "application/json", strings.NewReader(`{"client_id":"`+h.clientA.ID+`","capability":"`+capability+`"}`))
		if created.Code != http.StatusCreated {
			t.Fatalf("create job %d = %d %s", index, created.Code, created.Body.String())
		}
		var job struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		jobIDs[index] = job.ID
	}
	block := make(chan struct{})
	h.dialer.block.Store(&block)
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+jobIDs[0]+"/start", h.tokenA, "", nil); response.Code != http.StatusOK {
		t.Fatalf("start first job = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+jobIDs[1]+"/start", h.tokenA, "", nil); response.Code != http.StatusTooManyRequests {
		t.Fatalf("start beyond configured job limit = %d %s", response.Code, response.Body.String())
	}
	if response := h.request(http.MethodPost, "/api/v1/transfers/jobs/"+jobIDs[0]+"/cancel", h.tokenA, "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("cancel first job = %d %s", response.Code, response.Body.String())
	}
	close(block)
}
