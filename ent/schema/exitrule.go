package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ExitRule struct{ ent.Schema }

func (ExitRule) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("server_id").Immutable(),
		field.String("prefix").NotEmpty(),
		field.Uint16("start_port"),
		field.Uint16("end_port"),
		field.Bool("enabled").Default(true),
		createdAtField(),
		updatedAtField(),
	}
}

func (ExitRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("server_id", "prefix", "start_port", "end_port").Unique(),
		index.Fields("user_id"),
	}
}

func (ExitRule) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("exit_rules").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("server", TailServer.Type).Ref("exit_rules").Field("server_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
