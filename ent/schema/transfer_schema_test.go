package schema

import (
	"slices"
	"testing"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
)

const transferBlockSize = 8 * 1024 * 1024

func TestTransferSchemaDescriptors(t *testing.T) {
	t.Parallel()

	allStatuses := []string{"staging", "ready", "running", "completed", "failed", "canceled", "interrupted", "expired", "deleting"}
	manifestFields := []string{"id", "virtual_path", "size_bytes", "mtime", "blake3", "block_size", "block_hashes"}

	shareFields := fieldsByName(TransferShare{}.Fields())
	requireFields(t, shareFields, "id", "user_id", "server_id", "status", "capability_hash", "total_bytes", "file_count", "expires_at")
	requireSensitive(t, shareFields, "capability_hash")
	requireImmutable(t, shareFields, "id", "user_id", "server_id")
	requireEnum(t, shareFields, "status", allStatuses)
	requireNoPlaintextSecretsOrPaths(t, shareFields)
	requireEdge(t, TransferShare{}.Edges(), "owner", "User", "user_id")
	requireEdge(t, TransferShare{}.Edges(), "server", "TailServer", "server_id")
	requireAssociationCascade(t, TransferShare{}.Edges(), "files", "ShareFile")

	shareFileFields := fieldsByName(ShareFile{}.Fields())
	requireFields(t, shareFileFields, append([]string{"user_id", "share_id", "storage_name"}, manifestFields...)...)
	requireSensitive(t, shareFileFields, "storage_name")
	requireImmutable(t, shareFileFields, append([]string{"user_id", "share_id", "storage_name"}, manifestFields...)...)
	requireDefault(t, shareFileFields, "block_size", int64(transferBlockSize))
	requireNoPlaintextSecretsOrPaths(t, shareFileFields)
	requireEdge(t, ShareFile{}.Edges(), "owner", "User", "user_id")
	requireEdge(t, ShareFile{}.Edges(), "share", "TransferShare", "share_id")

	jobFields := fieldsByName(TransferJob{}.Fields())
	requireFields(t, jobFields, "id", "user_id", "client_id", "remote_share_id", "remote_capability_cipher", "status", "total_bytes", "received_bytes", "expires_at")
	requireSensitive(t, jobFields, "remote_capability_cipher")
	requireImmutable(t, jobFields, "id", "user_id", "client_id", "remote_share_id", "remote_capability_cipher")
	requireEnum(t, jobFields, "status", allStatuses)
	requireNoPlaintextSecretsOrPaths(t, jobFields)
	requireEdge(t, TransferJob{}.Edges(), "owner", "User", "user_id")
	requireEdge(t, TransferJob{}.Edges(), "client", "TailClient", "client_id")
	requireAssociationCascade(t, TransferJob{}.Edges(), "items", "TransferItem")

	itemFields := fieldsByName(TransferItem{}.Fields())
	requireFields(t, itemFields, append([]string{"user_id", "job_id", "remote_file_id", "storage_name", "completed_blocks", "received_bytes", "status"}, manifestFields[1:]...)...)
	requireSensitive(t, itemFields, "storage_name")
	requireImmutable(t, itemFields, append([]string{"user_id", "job_id", "remote_file_id", "storage_name"}, manifestFields[1:]...)...)
	requireDefault(t, itemFields, "block_size", int64(transferBlockSize))
	requireEnum(t, itemFields, "status", allStatuses)
	requireNoPlaintextSecretsOrPaths(t, itemFields)
	requireEdge(t, TransferItem{}.Edges(), "owner", "User", "user_id")
	requireEdge(t, TransferItem{}.Edges(), "job", "TransferJob", "job_id")

	requireAssociationCascade(t, User{}.Edges(), "transfer_shares", "TransferShare")
	requireAssociationCascade(t, User{}.Edges(), "share_files", "ShareFile")
	requireAssociationCascade(t, User{}.Edges(), "transfer_jobs", "TransferJob")
	requireAssociationCascade(t, User{}.Edges(), "transfer_items", "TransferItem")
	requireAssociationCascade(t, TailServer{}.Edges(), "transfer_shares", "TransferShare")
	requireAssociationCascade(t, TailClient{}.Edges(), "transfer_jobs", "TransferJob")
}

func TestTransferVirtualPathRejectsUnicodeControls(t *testing.T) {
	for _, path := range []string{"next\u0085line.txt", "folder/nested\u009fcontrol.txt", "folder/line\r\nbreak.txt"} {
		if err := validateVirtualPath(path); err == nil {
			t.Errorf("validateVirtualPath(%q) accepted a Unicode control", path)
		}
	}
	for _, path := range []string{"目录/报告.txt", `folder/héllo "quote".txt`} {
		if err := validateVirtualPath(path); err != nil {
			t.Errorf("validateVirtualPath(%q) = %v", path, err)
		}
	}
}

func fieldsByName(fields []ent.Field) map[string]*field.Descriptor {
	result := make(map[string]*field.Descriptor, len(fields))
	for _, candidate := range fields {
		descriptor := candidate.Descriptor()
		result[descriptor.Name] = descriptor
	}
	return result
}

func requireFields(t *testing.T, fields map[string]*field.Descriptor, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing field %q", name)
		}
	}
}

func requireSensitive(t *testing.T, fields map[string]*field.Descriptor, names ...string) {
	t.Helper()
	for _, name := range names {
		if descriptor := fields[name]; descriptor == nil || !descriptor.Sensitive {
			t.Errorf("field %q must be sensitive", name)
		}
	}
}

func requireImmutable(t *testing.T, fields map[string]*field.Descriptor, names ...string) {
	t.Helper()
	for _, name := range names {
		if descriptor := fields[name]; descriptor == nil || !descriptor.Immutable {
			t.Errorf("field %q must be immutable", name)
		}
	}
}

func requireDefault(t *testing.T, fields map[string]*field.Descriptor, name string, want int64) {
	t.Helper()
	descriptor := fields[name]
	if descriptor == nil || descriptor.Default != want {
		t.Errorf("field %q default = %#v, want %d", name, descriptor.Default, want)
	}
}

func requireEnum(t *testing.T, fields map[string]*field.Descriptor, name string, want []string) {
	t.Helper()
	descriptor := fields[name]
	if descriptor == nil {
		t.Errorf("missing enum %q", name)
		return
	}
	got := make([]string, 0, len(descriptor.Enums))
	for _, value := range descriptor.Enums {
		got = append(got, value.V)
	}
	if !slices.Equal(got, want) {
		t.Errorf("enum %q values = %q, want %q", name, got, want)
	}
}

func requireNoPlaintextSecretsOrPaths(t *testing.T, fields map[string]*field.Descriptor) {
	t.Helper()
	for _, forbidden := range []string{"capability", "storage_path", "source_path", "destination_path", "absolute_path"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("forbidden plaintext field %q", forbidden)
		}
	}
}

func requireEdge(t *testing.T, edges []ent.Edge, name, typ, fieldName string) {
	t.Helper()
	for _, candidate := range edges {
		descriptor := candidate.Descriptor()
		if descriptor.Name != name {
			continue
		}
		if descriptor.Type != typ || descriptor.Field != fieldName || !descriptor.Inverse || !descriptor.Unique || !descriptor.Required || !descriptor.Immutable {
			t.Errorf("edge %q descriptor = %+v", name, descriptor)
		}
		for _, annotation := range descriptor.Annotations {
			if sqlAnnotation, ok := annotation.(*entsql.Annotation); ok && sqlAnnotation.OnDelete == entsql.Cascade {
				return
			}
		}
		t.Errorf("edge %q is missing ON DELETE CASCADE", name)
		return
	}
	t.Errorf("missing edge %q", name)
}

func requireAssociationCascade(t *testing.T, edges []ent.Edge, name, typ string) {
	t.Helper()
	for _, candidate := range edges {
		descriptor := candidate.Descriptor()
		if descriptor.Name != name {
			continue
		}
		if descriptor.Type != typ || descriptor.Inverse {
			t.Errorf("edge %q descriptor = %+v", name, descriptor)
		}
		for _, annotation := range descriptor.Annotations {
			if sqlAnnotation, ok := annotation.(*entsql.Annotation); ok && sqlAnnotation.OnDelete == entsql.Cascade {
				return
			}
		}
		t.Errorf("edge %q is missing ON DELETE CASCADE", name)
		return
	}
	t.Errorf("missing edge %q", name)
}

func TestTransferParentIndexesAreScoped(t *testing.T) {
	t.Parallel()
	requireIndex(t, TransferShare{}.Indexes(), false, "user_id", "status", "expires_at")
	requireIndex(t, TransferShare{}.Indexes(), false, "status", "expires_at")
	requireUniqueIndex(t, ShareFile{}.Indexes(), "share_id", "virtual_path")
	requireIndex(t, TransferJob{}.Indexes(), false, "user_id", "status", "expires_at")
	requireIndex(t, TransferJob{}.Indexes(), false, "status", "expires_at")
	requireUniqueIndex(t, TransferItem{}.Indexes(), "job_id", "remote_file_id")
}

func requireUniqueIndex(t *testing.T, indexes []ent.Index, fields ...string) {
	requireIndex(t, indexes, true, fields...)
}

func requireIndex(t *testing.T, indexes []ent.Index, unique bool, fields ...string) {
	t.Helper()
	for _, candidate := range indexes {
		descriptor := candidate.Descriptor()
		if slices.Equal(descriptor.Fields, fields) && descriptor.Unique == unique {
			return
		}
	}
	t.Errorf("missing index on %q (unique=%t)", fields, unique)
}
