package tailnet

import (
	"context"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"

	_ "github.com/lib-x/entsqlite"
)

func TestParentDeletionCascadesOwnedChildren(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:cascade?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SaveX(ctx)
	db.PortMapping.Create().SetUserID(owner.ID).SetServerID(server.ID).SetName("web").SetListenPort(80).SetTargetPort(8080).SaveX(ctx)
	db.AllowedClient.Create().SetUserID(owner.ID).SetServerID(server.ID).SetName("laptop").SetPublicKey("nodekey:test").SaveX(ctx)
	db.ExitRule.Create().SetUserID(owner.ID).SetServerID(server.ID).SetPrefix("10.0.0.0/8").SetStartPort(443).SetEndPort(443).SaveX(ctx)
	if err := db.TailServer.DeleteOneID(server.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.PortMapping.Query().CountX(ctx); got != 0 {
		t.Fatalf("port mappings after server delete = %d", got)
	}
	if got := db.AllowedClient.Query().CountX(ctx); got != 0 {
		t.Fatalf("allowed clients after server delete = %d", got)
	}
	if got := db.ExitRule.Query().CountX(ctx); got != 0 {
		t.Fatalf("exit rules after server delete = %d", got)
	}

	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)
	db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("api").SetSlug("api-route").SetRemotePort(80).SaveX(ctx)
	if err := db.TailClient.DeleteOneID(client.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.PublishedRoute.Query().CountX(ctx); got != 0 {
		t.Fatalf("routes after client delete = %d", got)
	}

	server = db.TailServer.Create().SetUserID(owner.ID).SetName("owner-cascade").SaveX(ctx)
	db.ExitRule.Create().SetUserID(owner.ID).SetServerID(server.ID).SetPrefix("2001:db8::/32").SetStartPort(1).SetEndPort(65535).SaveX(ctx)
	if err := db.User.DeleteOneID(owner.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.ExitRule.Query().CountX(ctx); got != 0 {
		t.Fatalf("exit rules after owner delete = %d", got)
	}
}

func TestTransferParentDeletionCascadesMetadata(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transfer-cascade?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("transfer-owner").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("transfer-server").SaveX(ctx)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("transfer-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)

	share := createTransferShare(t, ctx, db, owner.ID, server.ID)
	createShareFile(t, ctx, db, owner.ID, share.ID)
	if err := db.TransferShare.DeleteOneID(share.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.ShareFile.Query().CountX(ctx); got != 0 {
		t.Fatalf("share files after share delete = %d", got)
	}

	share = createTransferShare(t, ctx, db, owner.ID, server.ID)
	createShareFile(t, ctx, db, owner.ID, share.ID)
	if err := db.TailServer.DeleteOneID(server.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.TransferShare.Query().CountX(ctx); got != 0 {
		t.Fatalf("shares after server delete = %d", got)
	}
	if got := db.ShareFile.Query().CountX(ctx); got != 0 {
		t.Fatalf("share files after server delete = %d", got)
	}

	job := createTransferJob(t, ctx, db, owner.ID, client.ID)
	createTransferItem(t, ctx, db, owner.ID, job.ID)
	if err := db.TransferJob.DeleteOneID(job.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.TransferItem.Query().CountX(ctx); got != 0 {
		t.Fatalf("transfer items after job delete = %d", got)
	}

	job = createTransferJob(t, ctx, db, owner.ID, client.ID)
	createTransferItem(t, ctx, db, owner.ID, job.ID)
	if err := db.TailClient.DeleteOneID(client.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.TransferJob.Query().CountX(ctx); got != 0 {
		t.Fatalf("jobs after client delete = %d", got)
	}
	if got := db.TransferItem.Query().CountX(ctx); got != 0 {
		t.Fatalf("transfer items after client delete = %d", got)
	}

	server = db.TailServer.Create().SetUserID(owner.ID).SetName("owner-transfer-server").SaveX(ctx)
	client = db.TailClient.Create().SetUserID(owner.ID).SetName("owner-transfer-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)
	share = createTransferShare(t, ctx, db, owner.ID, server.ID)
	createShareFile(t, ctx, db, owner.ID, share.ID)
	job = createTransferJob(t, ctx, db, owner.ID, client.ID)
	createTransferItem(t, ctx, db, owner.ID, job.ID)
	if err := db.User.DeleteOneID(owner.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.TransferShare.Query().CountX(ctx); got != 0 {
		t.Fatalf("shares after owner delete = %d", got)
	}
	if got := db.ShareFile.Query().CountX(ctx); got != 0 {
		t.Fatalf("share files after owner delete = %d", got)
	}
	if got := db.TransferJob.Query().CountX(ctx); got != 0 {
		t.Fatalf("jobs after owner delete = %d", got)
	}
	if got := db.TransferItem.Query().CountX(ctx); got != 0 {
		t.Fatalf("transfer items after owner delete = %d", got)
	}
}

func createTransferShare(t *testing.T, ctx context.Context, db *ent.Client, ownerID, serverID string) *ent.TransferShare {
	t.Helper()
	return db.TransferShare.Create().
		SetUserID(ownerID).
		SetServerID(serverID).
		SetCapabilityHash([]byte("sha256-capability-hash")).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SaveX(ctx)
}

func createShareFile(t *testing.T, ctx context.Context, db *ent.Client, ownerID, shareID string) *ent.ShareFile {
	t.Helper()
	return db.ShareFile.Create().
		SetUserID(ownerID).
		SetShareID(shareID).
		SetStorageName("outgoing-random-name").
		SetVirtualPath("report.txt").
		SetSizeBytes(4).
		SetMtime(time.Now()).
		SetBlake3("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").
		SetBlockHashes([]string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).
		SaveX(ctx)
}

func createTransferJob(t *testing.T, ctx context.Context, db *ent.Client, ownerID, clientID string) *ent.TransferJob {
	t.Helper()
	return db.TransferJob.Create().
		SetUserID(ownerID).
		SetClientID(clientID).
		SetRemoteShareID("01900000-0000-7000-8000-000000000001").
		SetRemoteCapabilityCipher([]byte("encrypted-capability")).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SaveX(ctx)
}

func createTransferItem(t *testing.T, ctx context.Context, db *ent.Client, ownerID, jobID string) *ent.TransferItem {
	t.Helper()
	return db.TransferItem.Create().
		SetUserID(ownerID).
		SetJobID(jobID).
		SetRemoteFileID("01900000-0000-7000-8000-000000000002").
		SetStorageName("incoming-random-name").
		SetVirtualPath("report.txt").
		SetSizeBytes(4).
		SetMtime(time.Now()).
		SetBlake3("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").
		SetBlockHashes([]string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).
		SaveX(ctx)
}
