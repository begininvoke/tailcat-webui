package audit

import (
	"testing"

	"github.com/ca-x/tailcat-webui/ent/enttest"

	_ "github.com/lib-x/entsqlite"
)

func TestRecordPersistsSecurityEvent(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:audit?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	user := db.User.Create().SetIssuer("test").SetSubject("operator").SaveX(t.Context())
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Record(t.Context(), Entry{UserID: user.ID, Action: "server.start", ResourceKind: "server", ResourceID: "server-id", Outcome: "success", RequestID: "request-id", Detail: "client_id=client-id"}); err != nil {
		t.Fatal(err)
	}
	record := db.AuditEvent.Query().OnlyX(t.Context())
	if record.UserID == nil || *record.UserID != user.ID || record.Action != "server.start" || record.RequestID != "request-id" || record.Detail != "client_id=client-id" {
		t.Fatalf("audit record = %#v", record)
	}
}

func TestRecordWithStableIDIsIdempotent(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:audit-idempotent?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	user := db.User.Create().SetIssuer("test").SetSubject("idempotent-operator").SaveX(t.Context())
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{ID: "run-id:diagnostic.succeeded", UserID: user.ID, Action: "diagnostic.succeeded", ResourceKind: "diagnostic", ResourceID: "run-id", Outcome: "success", Detail: "client_id=client-id"}
	if err := service.Record(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if err := service.Record(t.Context(), entry); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if got := db.AuditEvent.Query().CountX(t.Context()); got != 1 {
		t.Fatalf("audit rows = %d, want 1", got)
	}
}
