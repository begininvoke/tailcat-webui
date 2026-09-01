package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TransferJob is an incoming receive job for one selected TailClient. The
// remote share is protocol data, not a foreign key to a local TransferShare.
type TransferJob struct{ ent.Schema }

func (TransferJob) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("client_id").Immutable(),
		field.String("remote_share_id").NotEmpty().Immutable().Validate(validateUUIDv7),
		field.Bytes("remote_capability_cipher").NotEmpty().Immutable().Sensitive(),
		field.Enum("status").Values(transferStatuses...).Default("staging"),
		field.Int("attempt").NonNegative().Default(0),
		field.Enum("attempt_kind").Values("start", "retry", "resume").Optional(),
		field.Int64("total_bytes").NonNegative().Default(0),
		field.Int64("received_bytes").NonNegative().Default(0),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.Time("expires_at").Immutable(),
		field.Enum("error_code").Values(transferErrorCodes...).Optional(),
		createdAtField(),
		updatedAtField(),
	}
}

func (TransferJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("user_id", "status", "expires_at"),
		index.Fields("status", "expires_at"),
		index.Fields("client_id", "created_at"),
	}
}

func (TransferJob) Hooks() []ent.Hook { return []ent.Hook{validateTransferJobMutation} }

func (TransferJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("transfer_jobs").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("client", TailClient.Type).Ref("transfer_jobs").Field("client_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("items", TransferItem.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
