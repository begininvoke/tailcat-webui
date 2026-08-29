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
	if err := service.Record(t.Context(), Entry{UserID: user.ID, Action: "server.start", ResourceKind: "server", ResourceID: "server-id", Outcome: "success", RequestID: "request-id"}); err != nil {
		t.Fatal(err)
	}
	record := db.AuditEvent.Query().OnlyX(t.Context())
	if record.UserID == nil || *record.UserID != user.ID || record.Action != "server.start" || record.RequestID != "request-id" {
		t.Fatalf("audit record = %#v", record)
	}
}
