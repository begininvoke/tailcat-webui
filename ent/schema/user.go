package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type User struct{ ent.Schema }

func (User) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("issuer").NotEmpty().Immutable(),
		field.String("subject").NotEmpty().Immutable().Sensitive(),
		field.String("email").Optional(),
		field.String("display_name").Optional(),
		field.String("avatar_url").Optional(),
		createdAtField(),
		updatedAtField(),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{index.Fields("issuer", "subject").Unique()}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("sessions", Session.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("servers", TailServer.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("exit_rules", ExitRule.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("clients", TailClient.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("routes", PublishedRoute.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("audit_events", AuditEvent.Type).Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}
