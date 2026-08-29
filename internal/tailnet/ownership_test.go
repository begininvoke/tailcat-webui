package tailnet

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/ent/portmapping"
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
