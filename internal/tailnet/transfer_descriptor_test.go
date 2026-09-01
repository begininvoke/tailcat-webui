package tailnet

import (
	"slices"
	"testing"

	"entgo.io/ent"

	entschema "github.com/ca-x/tailcat-webui/ent/schema"
)

func TestTransferSchemaFieldsHaveExactReviewedAllowlists(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		fields []ent.Field
		want   []string
	}{
		"share": {
			fields: entschema.TransferShare{}.Fields(),
			want:   []string{"capability_generation", "capability_hash", "created_at", "error_code", "expires_at", "file_count", "finished_at", "id", "ready_at", "server_id", "status", "total_bytes", "updated_at", "user_id"},
		},
		"share file": {
			fields: entschema.ShareFile{}.Fields(),
			want:   []string{"blake3", "block_hashes", "block_size", "created_at", "id", "mtime", "share_id", "size_bytes", "storage_name", "user_id", "virtual_path"},
		},
		"job": {
			fields: entschema.TransferJob{}.Fields(),
			want:   []string{"attempt", "attempt_kind", "client_id", "created_at", "error_code", "expires_at", "finished_at", "id", "received_bytes", "remote_capability_cipher", "remote_share_id", "started_at", "status", "total_bytes", "updated_at", "user_id"},
		},
		"item": {
			fields: entschema.TransferItem{}.Fields(),
			want:   []string{"blake3", "block_hashes", "block_size", "completed_blocks", "created_at", "error_code", "finished_at", "id", "job_id", "mtime", "received_bytes", "remote_file_id", "size_bytes", "started_at", "status", "storage_name", "updated_at", "user_id", "virtual_path"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := make([]string, len(test.fields))
			for index, field := range test.fields {
				got[index] = field.Descriptor().Name
			}
			slices.Sort(got)
			if !slices.Equal(got, test.want) {
				t.Fatalf("fields = %q, want exact allowlist %q", got, test.want)
			}
		})
	}
}
