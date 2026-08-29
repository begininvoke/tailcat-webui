package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type AllowedClient struct{ ent.Schema }

func (AllowedClient) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("server_id").Immutable(),
		field.String("name").NotEmpty(),
		field.String("public_key").NotEmpty().Sensitive(),
		createdAtField(),
	}
}

func (AllowedClient) Indexes() []ent.Index {
	return []ent.Index{index.Fields("server_id", "public_key").Unique(), index.Fields("user_id")}
}

func (AllowedClient) Edges() []ent.Edge {
	return []ent.Edge{edge.From("server", TailServer.Type).Ref("allowed_clients").Field("server_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade))}
}
