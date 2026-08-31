package database

import (
	"testing"
)

func TestOpenAppliesTransferCompositeOwnershipForeignKeys(t *testing.T) {
	client, raw, err := Open(t.Context(), "file:database-transfer-composite-fks?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = raw.Close()
	})
	if _, err := client.User.Create().SetIssuer("test").SetSubject("database-runtime").Save(t.Context()); err != nil {
		t.Fatalf("database.Open client has uninitialized Ent runtime: %v", err)
	}

	for _, table := range []string{"transfer_shares", "share_files", "transfer_jobs", "transfer_items"} {
		table := table
		t.Run(table, func(t *testing.T) {
			rows, err := raw.Query("PRAGMA foreign_key_list(" + table + ")")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			parts := make(map[int]map[int]string)
			for rows.Next() {
				var id, sequence int
				var referencedTable, from, to, onUpdate, onDelete, match string
				if err := rows.Scan(&id, &sequence, &referencedTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
					t.Fatal(err)
				}
				if onDelete == "CASCADE" {
					if parts[id] == nil {
						parts[id] = make(map[int]string)
					}
					parts[id][sequence] = from + "->" + to
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, columns := range parts {
				if columns[0] == "user_id->user_id" && (columns[1] == "server_id->id" || columns[1] == "share_id->id" || columns[1] == "client_id->id" || columns[1] == "job_id->id") {
					found = true
				}
			}
			if !found {
				t.Fatal("missing composite ownership foreign key")
			}
		})
	}
}
