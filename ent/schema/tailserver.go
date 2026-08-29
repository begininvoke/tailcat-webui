package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TailServer struct{ ent.Schema }

func (TailServer) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("name").NotEmpty(),
		field.Enum("key_mode").Values("ephemeral", "saved").Default("ephemeral"),
		field.Bytes("key_cipher").Optional().Sensitive(),
		field.String("region").Default("auto"),
		field.String("derp_map_url").Optional(),
		field.Bool("allowlist_enabled").Default(false),
		field.Bool("exit_node_enabled").Default(false),
		field.Bool("desired_running").Default(false),
		createdAtField(),
		updatedAtField(),
	}
}

func (TailServer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "name").Unique()}
}

func (TailServer) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("servers").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("mappings", PortMapping.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("allowed_clients", AllowedClient.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
