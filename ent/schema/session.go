package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Session struct{ ent.Schema }

func (Session) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("token_hash").Unique().Immutable().Sensitive(),
		field.Time("expires_at"),
		field.Time("last_seen_at"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Session) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "expires_at"), index.Fields("expires_at")}
}

func (Session) Edges() []ent.Edge {
	return []ent.Edge{edge.From("user", User.Type).Ref("sessions").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade))}
}
