package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TransferItem persists immutable remote manifest identity and sparse block
// completion for an incoming file. storage_name is a random Storage-owned name.
type TransferItem struct{ ent.Schema }

func (TransferItem) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("job_id").Immutable(),
		field.String("remote_file_id").NotEmpty().Immutable(),
		field.String("storage_name").NotEmpty().Immutable().Sensitive(),
		field.String("virtual_path").NotEmpty().Immutable(),
		field.Int64("size_bytes").NonNegative().Immutable(),
		field.Time("mtime").Immutable(),
		field.String("blake3").NotEmpty().Immutable().Validate(validateBLAKE3),
		field.Int64("block_size").Default(transferBlockSizeBytes).Immutable().Validate(validateTransferBlockSize),
		field.Strings("block_hashes").Immutable().Validate(validateBLAKE3Blocks),
		field.Ints("completed_blocks").Default([]int{}),
		field.Int64("received_bytes").NonNegative().Default(0),
		field.Enum("status").Values(transferStatuses...).Default("staging"),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.Enum("error_code").Values(transferErrorCodes...).Optional(),
		createdAtField(),
		updatedAtField(),
	}
}

func (TransferItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("job_id", "created_at"),
		index.Fields("job_id", "remote_file_id").Unique(),
	}
}

func (TransferItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("transfer_items").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("job", TransferJob.Type).Ref("items").Field("job_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
