package tailnet

import (
	"testing"

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
