package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type AuditEvent struct{ ent.Schema }

func (AuditEvent) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Optional().Nillable().Immutable(),
		field.String("action").NotEmpty(),
		field.String("resource_kind").Optional(),
		field.String("resource_id").Optional(),
		field.Enum("outcome").Values("success", "failure").Default("success"),
		field.String("request_id").Optional(),
		field.String("detail").Optional(),
		createdAtField(),
	}
}

func (AuditEvent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "created_at"), index.Fields("created_at"), index.Fields("resource_kind", "resource_id")}
}

func (AuditEvent) Edges() []ent.Edge {
	return []ent.Edge{edge.From("user", User.Type).Ref("audit_events").Field("user_id").Unique().Immutable().Annotations(entsql.OnDelete(entsql.SetNull))}
}
