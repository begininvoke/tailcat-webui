package transfer

import (
	"bytes"
	"context"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/ent/transferitem"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/events"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/zeebo/blake3"

	_ "github.com/lib-x/entsqlite"
)

type transferDialerFunc func(context.Context, string, string, uint16) (net.Conn, error)

func (function transferDialerFunc) DialPort(ctx context.Context, ownerID, clientID string, port uint16) (net.Conn, error) {
	return function(ctx, ownerID, clientID, port)
}

type transferAuditFunc func(context.Context, audit.Entry) error

func (function transferAuditFunc) Record(ctx context.Context, entry audit.Entry) error {
	return function(ctx, entry)
}

type transferPublisherFunc func(string, events.Envelope)

func (function transferPublisherFunc) PublishEvent(ownerID string, event events.Envelope) {
	function(ownerID, event)
}

func TestServiceRejectsNilDependencies(t *testing.T) {
	db, storage, box, _, _, _ := newTransferServiceData(t)
	dialer := transferDialerFunc(func(context.Context, string, string, uint16) (net.Conn, error) { return nil, errors.New("unused") })
	auditor := transferAuditFunc(func(context.Context, audit.Entry) error { return nil })
	publisher := transferPublisherFunc(func(string, events.Envelope) {})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		name      string
		db        *ent.Client
		storage   *Storage
		box       *secrets.Box
		dialer    ClientDialer
		auditor   AuditRecorder
		publisher EventPublisher
		logger    *slog.Logger
	}{
		{name: "database", storage: storage, box: box, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger},
		{name: "storage", db: db, box: box, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger},
		{name: "box", db: db, storage: storage, dialer: dialer, auditor: auditor, publisher: publisher, logger: logger},
		{name: "dialer", db: db, storage: storage, box: box, auditor: auditor, publisher: publisher, logger: logger},
		{name: "auditor", db: db, storage: storage, box: box, dialer: dialer, publisher: publisher, logger: logger},
		{name: "publisher", db: db, storage: storage, box: box, dialer: dialer, auditor: auditor, logger: logger},
		{name: "logger", db: db, storage: storage, box: box, dialer: dialer, auditor: auditor, publisher: publisher},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewService(t.Context(), test.db, test.storage, test.box, test.dialer, test.auditor, test.publisher, test.logger); err == nil {
				t.Fatal("nil dependency was accepted")
			}
		})
	}
	unavailable, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(t.Context(), db, storage, unavailable, dialer, auditor, publisher, logger); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("unavailable secret box error = %v, want ErrUnavailable", err)
	}
}

func TestOutgoingShareManifestRangeServerBindingRotationAndExpiry(t *testing.T) {
	db, storage, box, owner, server, otherServer := newTransferServiceData(t)
	service := newTransferServiceForTest(t, db, storage, box)
	created, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID, ExpiresAt: time.Now().Add(500 * time.Millisecond)})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	row := db.TransferShare.GetX(t.Context(), created.ID)
	if bytes.Contains(row.CapabilityHash, []byte(created.Capability)) || len(row.CapabilityHash) != 32 {
		t.Fatalf("stored capability material = %x", row.CapabilityHash)
	}
	file, err := service.StageFile(t.Context(), owner.ID, created.ID, StageFileInput{
		VirtualPath: "folder/hello.txt",
		Size:        3,
		Body:        io.NopCloser(bytes.NewBufferString("abc")),
	})
	if err != nil {
		t.Fatalf("StageFile: %v", err)
	}
	if _, err := service.FinalizeShare(t.Context(), owner.ID, created.ID); err != nil {
		t.Fatalf("FinalizeShare: %v", err)
	}

	dial := handlerDial(t, service.ReservedHandler(server.ID))
	manifest, err := fetchManifest(t.Context(), dial, created.ID, created.Capability)
	if err != nil {
		t.Fatalf("fetchManifest: %v", err)
	}
	files := manifest.Files()
	if len(files) != 1 || files[0].FileID() != file.ID || files[0].VirtualPath() != "folder/hello.txt" || files[0].Size() != 3 {
		t.Fatalf("manifest files = %+v", files)
	}
	data, err := fetchRange(t.Context(), handlerDial(t, service.ReservedHandler(server.ID)), created.ID, created.Capability, file.ID, 0, 3)
	if err != nil || string(data) != "abc" {
		t.Fatalf("fetchRange data=%q error=%v", data, err)
	}
	if _, err := fetchRange(t.Context(), handlerDial(t, service.ReservedHandler(server.ID)), created.ID, created.Capability, file.ID, 1, 2); protocolCode(err) != CodeProtocolInvalid {
		t.Fatalf("non-block range error = %v, want %s", err, CodeProtocolInvalid)
	}
	if _, err := fetchManifest(t.Context(), handlerDial(t, service.ReservedHandler(otherServer.ID)), created.ID, created.Capability); protocolCode(err) != CodeInvalidCapability {
		t.Fatalf("wrong-server error = %v, want %s", err, CodeInvalidCapability)
	}
	rotated, err := service.RotateShare(t.Context(), owner.ID, created.ID)
	if err != nil {
		t.Fatalf("RotateShare: %v", err)
	}
	if rotated == created.Capability {
		t.Fatal("rotation reused the prior capability")
	}
	if _, err := fetchManifest(t.Context(), handlerDial(t, service.ReservedHandler(server.ID)), created.ID, created.Capability); protocolCode(err) != CodeInvalidCapability {
		t.Fatalf("old capability error = %v, want %s", err, CodeInvalidCapability)
	}
	if _, err := fetchManifest(t.Context(), handlerDial(t, service.ReservedHandler(server.ID)), created.ID, rotated); err != nil {
		t.Fatalf("rotated capability: %v", err)
	}

	time.Sleep(time.Until(created.ExpiresAt) + 10*time.Millisecond)
	if _, err := fetchManifest(t.Context(), handlerDial(t, service.ReservedHandler(server.ID)), created.ID, rotated); protocolCode(err) != CodeInvalidCapability {
		t.Fatalf("expired capability error = %v, want %s", err, CodeInvalidCapability)
	}
}

func TestJobCapabilityAssociatedDataBindsOwnerAndJob(t *testing.T) {
	_, _, box, owner, _, _ := newTransferServiceData(t)
	jobID := newEntityID()
	ciphertext, err := box.Seal([]byte("tcs1.secret"), jobCapabilityAAD(owner.ID, jobID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := box.Open(ciphertext, jobCapabilityAAD(newEntityID(), jobID)); err == nil {
		t.Fatal("cipher opened with wrong owner associated data")
	}
	if _, err := box.Open(ciphertext, jobCapabilityAAD(owner.ID, newEntityID())); err == nil {
		t.Fatal("cipher opened with wrong job associated data")
	}
	plaintext, err := box.Open(ciphertext, jobCapabilityAAD(owner.ID, jobID))
	if err != nil || string(plaintext) != "tcs1.secret" {
		t.Fatalf("Open associated-data-bound capability error=%v", err)
	}
}

func TestCapabilityAuthorizationAlwaysUsesConstantTimeDummyComparison(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	service := newTransferServiceForTest(t, db, storage, box)
	created, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	comparisons := make([][2]int, 0, 3)
	service.compareCapability = func(left, right []byte) int {
		mu.Lock()
		comparisons = append(comparisons, [2]int{len(left), len(right)})
		mu.Unlock()
		return 0
	}
	for _, request := range []wireRequest{
		{Version: protocolVersion, ShareID: created.ID, Capability: created.Capability, Operation: operationManifest},
		{Version: protocolVersion, ShareID: newEntityID(), Capability: created.Capability, Operation: operationManifest},
		{Version: protocolVersion, ShareID: created.ID, Capability: "not-a-capability", Operation: operationManifest},
	} {
		if _, err := service.authorizeRequest(t.Context(), server.ID, request); protocolCode(err) != CodeInvalidCapability {
			t.Fatalf("authorizeRequest error = %v, want invalid capability", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(comparisons) != 3 {
		t.Fatalf("constant-time comparisons = %d, want 3", len(comparisons))
	}
	for _, lengths := range comparisons {
		if lengths != [2]int{32, 32} {
			t.Fatalf("comparison lengths = %v, want [32 32]", lengths)
		}
	}
}

func TestCreateIncomingJobEncryptsCapabilityAndCreatesRootedPartialsTransactionally(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("receiver").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
	service := newLoopbackTransferService(t, db, storage, box, owner.ID, client.ID, server.ID)
	share, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if _, err := service.StageFile(t.Context(), owner.ID, share.ID, StageFileInput{VirtualPath: "hello.txt", Size: 3, Body: io.NopCloser(bytes.NewBufferString("abc"))}); err != nil {
		t.Fatalf("StageFile: %v", err)
	}
	if _, err := service.FinalizeShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatalf("FinalizeShare: %v", err)
	}

	job, err := service.CreateIncomingJob(t.Context(), owner.ID, CreateIncomingJobInput{ClientID: client.ID, Capability: share.Capability})
	if err != nil {
		t.Fatalf("CreateIncomingJob: %v", err)
	}
	row := db.TransferJob.GetX(t.Context(), job.ID)
	if row.Status != transferjob.StatusReady || row.RemoteShareID != share.ID || row.TotalBytes != 3 || row.ReceivedBytes != 0 {
		t.Fatalf("incoming job row = %+v", row)
	}
	if bytes.Contains(row.RemoteCapabilityCipher, []byte(share.Capability)) {
		t.Fatal("incoming capability was stored in plaintext")
	}
	plaintext, err := box.Open(row.RemoteCapabilityCipher, jobCapabilityAAD(owner.ID, row.ID))
	if err != nil || string(plaintext) != share.Capability {
		t.Fatalf("decrypt capability error=%v", err)
	}
	items := db.TransferItem.Query().Where(transferitem.JobIDEQ(job.ID), transferitem.UserIDEQ(owner.ID)).AllX(t.Context())
	if len(items) != 1 || items[0].Status != transferitem.StatusReady || items[0].SizeBytes != 3 || items[0].VirtualPath != "hello.txt" {
		t.Fatalf("incoming items = %+v", items)
	}
	partial, err := storage.OpenPartial(t.Context(), owner.ID, job.ID, items[0].StorageName, items[0].SizeBytes)
	if err != nil {
		t.Fatalf("OpenPartial: %v", err)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("Close partial: %v", err)
	}
}

func TestJobRunnerUsesExactlyFourWorkersAndCompletesOnlyAfterWholeHash(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("runner").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
	service := newLoopbackTransferService(t, db, storage, box, owner.ID, client.ID, server.ID)
	payload := bytes.Repeat([]byte("x"), int(BlockSize)+3)
	share, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageFile(t.Context(), owner.ID, share.ID, StageFileInput{VirtualPath: "large.bin", Size: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}); err != nil {
		t.Fatalf("StageFile: %v", err)
	}
	if _, err := service.FinalizeShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	job, err := service.CreateIncomingJob(t.Context(), owner.ID, CreateIncomingJobInput{ClientID: client.ID, Capability: share.Capability})
	if err != nil {
		t.Fatalf("CreateIncomingJob: %v", err)
	}
	var started atomic.Int64
	var stopped atomic.Int64
	var active atomic.Int64
	var maximum atomic.Int64
	service.runnerHooks.workerStarted = func() {
		started.Add(1)
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
	}
	service.runnerHooks.workerStopped = func() {
		active.Add(-1)
		stopped.Add(1)
	}
	if _, err := service.StartJob(t.Context(), owner.ID, job.ID); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		row := db.TransferJob.GetX(t.Context(), job.ID)
		if row.Status == transferjob.StatusCompleted {
			if row.ReceivedBytes != int64(len(payload)) || row.FinishedAt == nil {
				t.Fatalf("completed job = %+v", row)
			}
			break
		}
		if row.Status == transferjob.StatusFailed || row.Status == transferjob.StatusCanceled || row.Status == transferjob.StatusInterrupted {
			t.Fatalf("job ended as %s with %s", row.Status, row.ErrorCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete; status=%s received=%d", row.Status, row.ReceivedBytes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if started.Load() != 4 || stopped.Load() != 4 || active.Load() != 0 || maximum.Load() > 4 {
		t.Fatalf("worker lifecycle started=%d stopped=%d active=%d max=%d", started.Load(), stopped.Load(), active.Load(), maximum.Load())
	}
	item := db.TransferItem.Query().Where(transferitem.JobIDEQ(job.ID)).OnlyX(t.Context())
	if item.Status != transferitem.StatusCompleted || item.ReceivedBytes != int64(len(payload)) || len(item.CompletedBlocks) != 2 {
		t.Fatalf("completed item = %+v", item)
	}
	manifest, err := storage.BuildFileManifest(t.Context(), owner.ID, job.ID, item.StorageName, item.ID, item.VirtualPath)
	if err != nil || manifest.BLAKE3() != item.Blake3 {
		t.Fatalf("final manifest hash=%q want=%q error=%v", manifest.BLAKE3(), item.Blake3, err)
	}
}

func TestShareRotationCancelsActiveHandlerAndDeleteRetriesStorageFailure(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	service := newTransferServiceForTest(t, db, storage, box)
	share, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	file, err := service.StageFile(t.Context(), owner.ID, share.ID, StageFileInput{VirtualPath: "file.txt", Size: 3, Body: io.NopCloser(bytes.NewBufferString("abc"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	client, serverConn := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		service.ReservedHandler(server.ID)(t.Context(), serverConn)
		close(handlerDone)
	}()
	if err := writeRequest(t.Context(), client, wireRequest{Version: protocolVersion, ShareID: share.ID, Capability: share.Capability, Operation: operationRange, FileID: file.ID, Offset: 0, Length: 3}); err != nil {
		t.Fatalf("writeRequest: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		active := len(service.streams[share.ID])
		service.mu.Unlock()
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handler stream was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.RotateShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatalf("RotateShare: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rotation did not close active handler stream")
	}
	_ = client.Close()

	removeFailure := errors.New("remove staged file failed")
	storage.hooks.remove = func(*os.Root, string) error { return removeFailure }
	if err := service.DeleteShare(t.Context(), owner.ID, share.ID); !errors.Is(err, removeFailure) {
		t.Fatalf("DeleteShare error = %v, want removal failure", err)
	}
	if status := db.TransferShare.GetX(t.Context(), share.ID).Status; status != transfershare.StatusDeleting {
		t.Fatalf("share status after failed delete = %s, want deleting", status)
	}
	storage.hooks.remove = (*os.Root).Remove
	if err := service.DeleteShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatalf("retry DeleteShare: %v", err)
	}
	if _, err := db.TransferShare.Get(t.Context(), share.ID); !ent.IsNotFound(err) {
		t.Fatalf("share metadata remains after retry: %v", err)
	}
}

func TestTwoActiveJobsPerOwnerAreReservedBeforeWorkersAndCancelJoins(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("capacity").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
	service := newLoopbackTransferService(t, db, storage, box, owner.ID, client.ID, server.ID)
	share, err := service.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageFile(t.Context(), owner.ID, share.ID, StageFileInput{VirtualPath: "file.txt", Size: 3, Body: io.NopCloser(bytes.NewBufferString("abc"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	jobs := make([]JobView, 3)
	for index := range jobs {
		jobs[index], err = service.CreateIncomingJob(t.Context(), owner.ID, CreateIncomingJobInput{ClientID: client.ID, Capability: share.Capability})
		if err != nil {
			t.Fatalf("CreateIncomingJob %d: %v", index, err)
		}
	}
	service.dialer = transferDialerFunc(func(ctx context.Context, _, _ string, port uint16) (net.Conn, error) {
		if port != ReservedPort {
			return nil, errors.New("wrong port")
		}
		clientConn, peer := net.Pipe()
		go func() {
			<-ctx.Done()
			_ = peer.Close()
		}()
		return clientConn, nil
	})
	for index := range 2 {
		if _, err := service.StartJob(t.Context(), owner.ID, jobs[index].ID); err != nil {
			t.Fatalf("StartJob %d: %v", index, err)
		}
	}
	if _, err := service.StartJob(t.Context(), owner.ID, jobs[2].ID); !errors.Is(err, ErrOwnerCapacity) {
		t.Fatalf("third StartJob error = %v, want ErrOwnerCapacity", err)
	}
	for index := range 2 {
		if err := service.CancelJob(t.Context(), owner.ID, jobs[index].ID); err != nil {
			t.Fatalf("CancelJob %d: %v", index, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for index := range 2 {
		for {
			row := db.TransferJob.GetX(t.Context(), jobs[index].ID)
			if row.Status == transferjob.StatusCanceled {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %d did not cancel; status=%s", index, row.Status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for {
		service.mu.Lock()
		activeJobs := len(service.activeJobs)
		ownerJobs := service.ownerJobs[owner.ID]
		service.mu.Unlock()
		if activeJobs == 0 && ownerJobs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admission after cancel active=%d owner=%d", activeJobs, ownerJobs)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLegalTransferTransitionsRejectImpossibleEnumEdges(t *testing.T) {
	for _, test := range []struct {
		from string
		to   string
		want bool
	}{
		{from: "staging", to: "ready", want: true},
		{from: "ready", to: "running", want: true},
		{from: "running", to: "completed", want: true},
		{from: "interrupted", to: "running", want: true},
		{from: "completed", to: "running", want: false},
		{from: "failed", to: "completed", want: false},
		{from: "deleting", to: "ready", want: false},
		{from: "unknown", to: "running", want: false},
	} {
		if got := legalTransferTransition(test.from, test.to); got != test.want {
			t.Fatalf("legalTransferTransition(%q, %q) = %v, want %v", test.from, test.to, got, test.want)
		}
	}
}

func TestRunnerRejectsBadBlockBeforeWriteAndWholeHashBeforeCompletion(t *testing.T) {
	for _, test := range []struct {
		name                  string
		wholeHash             string
		rangeBody             string
		wantReceived          int64
		wantCompleted         int
		wantProgressAfterSync bool
	}{
		{name: "block hash", wholeHash: blake3Hex("abc"), rangeBody: "xyz", wantReceived: 0, wantCompleted: 0},
		{name: "whole hash", wholeHash: blake3Hex("different"), rangeBody: "abc", wantReceived: 3, wantCompleted: 1, wantProgressAfterSync: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, storage, box, owner, _, _ := newTransferServiceData(t)
			client := db.TailClient.Create().SetUserID(owner.ID).SetName("integrity").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
			remoteShareID := newEntityID()
			remoteFileID := newEntityID()
			capability, _, err := newCapability(remoteShareID)
			if err != nil {
				t.Fatal(err)
			}
			manifest := manifestWire{
				Version: protocolVersion, ShareID: remoteShareID, BlockSize: BlockSize,
				Files: []manifestFileWire{{
					FileID: remoteFileID, VirtualPath: "file.bin", Size: 3,
					MTime: time.Unix(1, 0).UTC().Format(time.RFC3339Nano), BLAKE3: test.wholeHash,
					BlockSize: BlockSize, BlockHashes: []string{blake3Hex("abc")},
				}},
			}
			dialer := transferDialerFunc(func(ctx context.Context, ownerID, clientID string, port uint16) (net.Conn, error) {
				if ownerID != owner.ID || clientID != client.ID || port != ReservedPort {
					return nil, errors.New("unexpected integrity-test dial")
				}
				peer, server := net.Pipe()
				go func() {
					defer server.Close()
					request, err := readRequest(ctx, server)
					if err != nil {
						return
					}
					switch request.Operation {
					case operationManifest:
						payload, _ := json.Marshal(&manifest)
						_ = writeSuccessResponse(ctx, server, payload, MaxManifestResponseBytes)
					case operationRange:
						_ = writeSuccessResponse(ctx, server, []byte(test.rangeBody), MaxRangeResponseBytes)
					}
				}()
				return peer, nil
			})
			service, err := NewService(t.Context(), db, storage, box, dialer,
				transferAuditFunc(func(context.Context, audit.Entry) error { return nil }),
				transferPublisherFunc(func(string, events.Envelope) {}),
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			var synced atomic.Bool
			var progressAfterSync atomic.Bool
			service.runnerHooks.afterBlockSync = func(string, int) { synced.Store(true) }
			service.runnerHooks.beforeProgressSave = func(string, int) { progressAfterSync.Store(synced.Load()) }
			job, err := service.CreateIncomingJob(t.Context(), owner.ID, CreateIncomingJobInput{ClientID: client.ID, Capability: capability})
			if err != nil {
				t.Fatalf("CreateIncomingJob: %v", err)
			}
			if _, err := service.StartJob(t.Context(), owner.ID, job.ID); err != nil {
				t.Fatalf("StartJob: %v", err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				row := db.TransferJob.GetX(t.Context(), job.ID)
				if row.Status == transferjob.StatusFailed {
					if row.ErrorCode != transferjob.ErrorCodeTransferIntegrityMismatch {
						t.Fatalf("job error code = %s", row.ErrorCode)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("job did not fail; status=%s", row.Status)
				}
				time.Sleep(10 * time.Millisecond)
			}
			item := db.TransferItem.Query().Where(transferitem.JobIDEQ(job.ID)).OnlyX(t.Context())
			if item.ReceivedBytes != test.wantReceived || len(item.CompletedBlocks) != test.wantCompleted || item.Status != transferitem.StatusFailed {
				t.Fatalf("failed item = %+v", item)
			}
			if progressAfterSync.Load() != test.wantProgressAfterSync {
				t.Fatalf("progress-after-sync = %v, want %v", progressAfterSync.Load(), test.wantProgressAfterSync)
			}
			if test.wantReceived == 0 {
				handle, err := storage.Open(t.Context(), owner.ID, job.ID, item.StorageName)
				if err != nil {
					t.Fatal(err)
				}
				data := make([]byte, 3)
				_, readErr := handle.ReadAt(data, 0)
				_ = handle.Close()
				if readErr != nil || !bytes.Equal(data, []byte{0, 0, 0}) {
					t.Fatalf("bad block reached partial data=%v error=%v", data, readErr)
				}
			}
		})
	}
}

func TestStartupInterruptsAbandonedJobThenRecoveryResumesIt(t *testing.T) {
	db, storage, box, owner, server, _ := newTransferServiceData(t)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("restart").SetServerTokenCipher([]byte("cipher")).SetTokenHint("hint").SaveX(t.Context())
	first := newLoopbackTransferService(t, db, storage, box, owner.ID, client.ID, server.ID)
	share, err := first.CreateShare(t.Context(), owner.ID, CreateShareInput{ServerID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.StageFile(t.Context(), owner.ID, share.ID, StageFileInput{VirtualPath: "resume.txt", Size: 3, Body: io.NopCloser(bytes.NewBufferString("abc"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.FinalizeShare(t.Context(), owner.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	job, err := first.CreateIncomingJob(t.Context(), owner.ID, CreateIncomingJobInput{ClientID: client.ID, Capability: share.Capability})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	db.TransferJob.UpdateOneID(job.ID).SetStatus(transferjob.StatusRunning).SetStartedAt(now).ExecX(t.Context())
	db.TransferItem.Query().Where(transferitem.JobIDEQ(job.ID)).OnlyX(t.Context()).Update().SetStatus(transferitem.StatusRunning).SetStartedAt(now).ExecX(t.Context())

	var second *Service
	dialer := transferDialerFunc(func(ctx context.Context, gotOwnerID, gotClientID string, port uint16) (net.Conn, error) {
		if gotOwnerID != owner.ID || gotClientID != client.ID || port != ReservedPort {
			return nil, errors.New("unexpected restart dial")
		}
		return handlerDial(t, second.ReservedHandler(server.ID))(ctx)
	})
	second, err = NewService(t.Context(), db, storage, box, dialer,
		transferAuditFunc(func(context.Context, audit.Entry) error { return nil }),
		transferPublisherFunc(func(string, events.Envelope) {}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewService restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if status := db.TransferJob.GetX(t.Context(), job.ID).Status; status != transferjob.StatusInterrupted {
		t.Fatalf("startup job status = %s, want interrupted", status)
	}
	if status := db.TransferItem.Query().Where(transferitem.JobIDEQ(job.ID)).OnlyX(t.Context()).Status; status != transferitem.StatusInterrupted {
		t.Fatalf("startup item status = %s, want interrupted", status)
	}
	if err := second.RecoverAfterRestore(t.Context()); err != nil {
		t.Fatalf("RecoverAfterRestore: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := db.TransferJob.GetX(t.Context(), job.ID)
		if row.Status == transferjob.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed job did not complete; status=%s", row.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func blake3Hex(value string) string {
	hash := blake3.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func newTransferServiceData(t *testing.T) (*ent.Client, *Storage, *secrets.Box, *ent.User, *ent.TailServer, *ent.TailServer) {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+t.Name()+"-"+newEntityID()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject(t.Name()).SaveX(t.Context())
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SetRegion("tailcat.dev").SaveX(t.Context())
	otherServer := db.TailServer.Create().SetUserID(owner.ID).SetName("other").SetRegion("tailcat.dev").SaveX(t.Context())
	storage, err := NewStorage(filepath.Join(t.TempDir(), "transfer"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secrets.NewBox(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return db, storage, box, owner, server, otherServer
}

func newTransferServiceForTest(t *testing.T, db *ent.Client, storage *Storage, box *secrets.Box) *Service {
	t.Helper()
	service, err := NewService(t.Context(), db, storage, box,
		transferDialerFunc(func(context.Context, string, string, uint16) (net.Conn, error) { return nil, errors.New("unused") }),
		transferAuditFunc(func(context.Context, audit.Entry) error { return nil }),
		transferPublisherFunc(func(string, events.Envelope) {}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Service.Close: %v", err)
		}
	})
	return service
}

func newLoopbackTransferService(t *testing.T, db *ent.Client, storage *Storage, box *secrets.Box, ownerID, clientID, serverID string) *Service {
	t.Helper()
	var service *Service
	dialer := transferDialerFunc(func(ctx context.Context, gotOwnerID, gotClientID string, port uint16) (net.Conn, error) {
		if gotOwnerID != ownerID || gotClientID != clientID || port != ReservedPort {
			return nil, errors.New("unexpected transfer dial target")
		}
		return handlerDial(t, service.ReservedHandler(serverID))(ctx)
	})
	var err error
	service, err = NewService(t.Context(), db, storage, box, dialer,
		transferAuditFunc(func(context.Context, audit.Entry) error { return nil }),
		transferPublisherFunc(func(string, events.Envelope) {}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Service.Close: %v", err)
		}
	})
	return service
}

func handlerDial(t *testing.T, handler func(context.Context, net.Conn)) protocolDial {
	t.Helper()
	return func(ctx context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go handler(ctx, server)
		return client, nil
	}
}
