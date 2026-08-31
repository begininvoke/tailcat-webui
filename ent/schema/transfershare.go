package schema

import (
	"fmt"
	"regexp"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

const transferBlockSizeBytes int64 = 8 * 1024 * 1024

var blake3Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

var transferStatuses = []string{
	"staging",
	"ready",
	"running",
	"completed",
	"failed",
	"canceled",
	"interrupted",
	"expired",
	"deleting",
}

var transferErrorCodes = []string{
	"transfer_canceled",
	"transfer_expired",
	"transfer_remote_unavailable",
	"transfer_invalid_capability",
	"transfer_share_not_found",
	"transfer_protocol_invalid",
	"transfer_integrity_mismatch",
	"transfer_storage_failed",
	"transfer_limit_exceeded",
}

func validateTransferBlockSize(size int64) error {
	if size != transferBlockSizeBytes {
		return fmt.Errorf("transfer block size must be %d bytes", transferBlockSizeBytes)
	}
	return nil
}

func validateBLAKE3(hash string) error {
	if !blake3Hex.MatchString(hash) {
		return fmt.Errorf("BLAKE3 hash must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validateBLAKE3Blocks(hashes []string) error {
	for _, hash := range hashes {
		if err := validateBLAKE3(hash); err != nil {
			return err
		}
	}
	return nil
}

// TransferShare is an outgoing, expiring capability-gated manifest on one
// selected TailServer. Its capability hash is the only capability material
// retained in SQLite; the plaintext code is intentionally never persisted.
type TransferShare struct{ ent.Schema }

func (TransferShare) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("server_id").Immutable(),
		field.Enum("status").Values(transferStatuses...).Default("staging"),
		field.Bytes("capability_hash").NotEmpty().Sensitive(),
		field.Int64("total_bytes").NonNegative().Default(0),
		field.Int("file_count").NonNegative().Default(0),
		field.Time("ready_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.Time("expires_at").Immutable(),
		field.Enum("error_code").Values(transferErrorCodes...).Optional(),
		createdAtField(),
		updatedAtField(),
	}
}

func (TransferShare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("user_id", "status", "expires_at"),
		index.Fields("server_id", "created_at"),
	}
}

func (TransferShare) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("transfer_shares").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("server", TailServer.Type).Ref("transfer_shares").Field("server_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("files", ShareFile.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
