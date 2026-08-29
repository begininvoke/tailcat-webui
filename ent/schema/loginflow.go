package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type LoginFlow struct{ ent.Schema }

func (LoginFlow) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("state_hash").Unique().Immutable().Sensitive(),
		field.String("nonce").Immutable().Sensitive(),
		field.String("code_verifier").Immutable().Sensitive(),
		field.String("return_to").Default("/"),
		field.Time("expires_at"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (LoginFlow) Indexes() []ent.Index {
	return []ent.Index{index.Fields("expires_at")}
}
