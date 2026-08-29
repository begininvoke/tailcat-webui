package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type PortMapping struct{ ent.Schema }

func (PortMapping) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("server_id").Immutable(),
		field.String("name").NotEmpty(),
		field.Enum("kind").Values("tcp", "no_auth_ssh").Default("tcp"),
		field.Uint16("listen_port"),
		field.String("target_host").Default("127.0.0.1"),
		field.Uint16("target_port"),
		field.Bool("enabled").Default(true),
		createdAtField(),
		updatedAtField(),
	}
}

func (PortMapping) Indexes() []ent.Index {
	return []ent.Index{index.Fields("server_id", "listen_port").Unique(), index.Fields("user_id")}
}

func (PortMapping) Edges() []ent.Edge {
	return []ent.Edge{edge.From("server", TailServer.Type).Ref("mappings").Field("server_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade))}
}
