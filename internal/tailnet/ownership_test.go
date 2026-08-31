package tailnet

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/ent/portmapping"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
	"github.com/ca-x/tailcat-webui/ent/transferitem"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
	"github.com/ca-x/tailcat-webui/ent/transfershare"
	"github.com/ca-x/tailcat-webui/internal/secrets"

	_ "github.com/lib-x/entsqlite"
)

func TestCrossOwnerMutationIsHidden(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:ownership?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(ctx)
	attacker := db.User.Create().SetIssuer("test").SetSubject("attacker").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SaveX(ctx)
	mapping := db.PortMapping.Create().SetUserID(owner.ID).SetServerID(server.ID).SetName("web").SetListenPort(80).SetTargetPort(8080).SaveX(ctx)
	box, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, box, NewTargetPolicy(nil), NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteMapping(ctx, attacker.ID, mapping.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner delete error = %v", err)
	}
	if !db.PortMapping.Query().Where(portmapping.IDEQ(mapping.ID)).ExistX(ctx) {
		t.Fatal("cross-owner delete removed the mapping")
	}
}

func TestExitRulesAreNormalizedAndOwnerScoped(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:exit-rule-ownership?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("exit-owner").SaveX(ctx)
	attacker := db.User.Create().SetIssuer("test").SetSubject("exit-attacker").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SaveX(ctx)
	box, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, box, NewTargetPolicy(nil), NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	rule, err := manager.CreateExitRule(ctx, owner.ID, server.ID, CreateExitRuleInput{Prefix: "10.1.2.3/8", StartPort: 400, EndPort: 500, Enabled: true})
	if err != nil {
		t.Fatalf("CreateExitRule: %v", err)
	}
	if rule.Prefix != "10.0.0.0/8" {
		t.Fatalf("normalized prefix = %q, want 10.0.0.0/8", rule.Prefix)
	}
	if _, err := manager.ListExitRules(ctx, attacker.ID, server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner list error = %v", err)
	}
	if err := manager.DeleteExitRule(ctx, attacker.ID, rule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner delete error = %v", err)
	}
	if _, err := manager.SetExitNodeEnabled(ctx, attacker.ID, server.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner enable error = %v", err)
	}
	rules, err := manager.ListExitRules(ctx, owner.ID, server.ID)
	if err != nil {
		t.Fatalf("ListExitRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("owner rules = %+v, want rule %s", rules, rule.ID)
	}
}

func TestTransferOwnerAndParentPredicatesDoNotCrossTenants(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transfer-ownership?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("transfer-owner").SaveX(ctx)
	other := db.User.Create().SetIssuer("test").SetSubject("transfer-other").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("owner-server").SaveX(ctx)
	otherServer := db.TailServer.Create().SetUserID(other.ID).SetName("other-server").SaveX(ctx)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("owner-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)
	otherClient := db.TailClient.Create().SetUserID(other.ID).SetName("other-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)

	share := createTransferShare(t, ctx, db, owner.ID, server.ID)
	file := createShareFile(t, ctx, db, owner.ID, share.ID)
	job := createTransferJob(t, ctx, db, owner.ID, client.ID)
	item := createTransferItem(t, ctx, db, owner.ID, job.ID)
	otherShare := createTransferShare(t, ctx, db, other.ID, otherServer.ID)
	otherJob := createTransferJob(t, ctx, db, other.ID, otherClient.ID)

	if got := db.TransferShare.Query().Where(transfershare.UserIDEQ(owner.ID), transfershare.ServerIDEQ(server.ID)).OnlyX(ctx).ID; got != share.ID {
		t.Fatalf("owner share = %q, want %q", got, share.ID)
	}
	if db.TransferShare.Query().Where(transfershare.UserIDEQ(owner.ID), transfershare.ServerIDEQ(otherServer.ID)).ExistX(ctx) {
		t.Fatal("owner query returned a share from another owner's server")
	}
	if got := db.ShareFile.Query().Where(sharefile.UserIDEQ(owner.ID), sharefile.ShareIDEQ(share.ID)).OnlyX(ctx).ID; got != file.ID {
		t.Fatalf("owner share file = %q, want %q", got, file.ID)
	}
	if db.ShareFile.Query().Where(sharefile.UserIDEQ(owner.ID), sharefile.ShareIDEQ(otherShare.ID)).ExistX(ctx) {
		t.Fatal("owner query returned another owner's share file")
	}
	if got := db.TransferJob.Query().Where(transferjob.UserIDEQ(owner.ID), transferjob.ClientIDEQ(client.ID)).OnlyX(ctx).ID; got != job.ID {
		t.Fatalf("owner job = %q, want %q", got, job.ID)
	}
	if db.TransferJob.Query().Where(transferjob.UserIDEQ(owner.ID), transferjob.ClientIDEQ(otherClient.ID)).ExistX(ctx) {
		t.Fatal("owner query returned a job from another owner's client")
	}
	if got := db.TransferItem.Query().Where(transferitem.UserIDEQ(owner.ID), transferitem.JobIDEQ(job.ID)).OnlyX(ctx).ID; got != item.ID {
		t.Fatalf("owner item = %q, want %q", got, item.ID)
	}
	if db.TransferItem.Query().Where(transferitem.UserIDEQ(owner.ID), transferitem.JobIDEQ(otherJob.ID)).ExistX(ctx) {
		t.Fatal("owner query returned another owner's transfer item")
	}
}
