package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TailClient struct{ ent.Schema }

func (TailClient) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("name").NotEmpty(),
		field.Bytes("server_token_cipher").NotEmpty().Sensitive(),
		field.String("token_hint").NotEmpty(),
		field.Bytes("key_cipher").Optional().Sensitive(),
		field.String("derp_map_url").Optional(),
		field.Int64("last_ping_ms").Optional().Nillable(),
		field.String("last_path").Optional(),
		field.Time("last_ping_at").Optional().Nillable(),
		createdAtField(),
		updatedAtField(),
	}
}

func (TailClient) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "name").Unique()}
}

func (TailClient) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("clients").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("diagnostic_runs", DiagnosticRun.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("routes", PublishedRoute.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
