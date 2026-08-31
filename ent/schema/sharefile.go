package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ShareFile is an immutable staged outgoing file. storage_name is a random
// Storage-owned name, never a browser request path or host filesystem path.
type ShareFile struct{ ent.Schema }

func (ShareFile) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("share_id").Immutable(),
		field.String("storage_name").NotEmpty().Immutable().Sensitive(),
		field.String("virtual_path").NotEmpty().Immutable(),
		field.Int64("size_bytes").NonNegative().Immutable(),
		field.Time("mtime").Immutable(),
		field.String("blake3").NotEmpty().Immutable().Validate(validateBLAKE3),
		field.Int64("block_size").Default(transferBlockSizeBytes).Immutable().Validate(validateTransferBlockSize),
		field.Strings("block_hashes").Immutable().Validate(validateBLAKE3Blocks),
		createdAtField(),
	}
}

func (ShareFile) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("share_id", "created_at"),
		index.Fields("share_id", "virtual_path").Unique(),
	}
}

func (ShareFile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("share_files").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("share", TransferShare.Type).Ref("files").Field("share_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
