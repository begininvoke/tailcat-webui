package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type PublishedRoute struct{ ent.Schema }

func (PublishedRoute) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("client_id").Immutable(),
		field.String("name").NotEmpty(),
		field.String("slug").NotEmpty().Unique(),
		field.Uint16("remote_port"),
		field.String("base_path").Default("/"),
		field.Enum("access").Values("private", "public").Default("private"),
		field.Strings("allowed_methods").Default([]string{"GET", "HEAD"}),
		field.Bool("enabled").Default(true),
		createdAtField(),
		updatedAtField(),
	}
}

func (PublishedRoute) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id"), index.Fields("client_id")}
}

func (PublishedRoute) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("routes").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("client", TailClient.Type).Ref("routes").Field("client_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
