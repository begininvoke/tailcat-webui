package schema

import (
	"time"
	"uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

func idField() ent.Field {
	return field.String("id").DefaultFunc(func() string { return uuid.NewV7().String() }).Immutable()
}

func createdAtField() ent.Field {
	return field.Time("created_at").Default(time.Now).Immutable()
}

func updatedAtField() ent.Field {
	return field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now)
}
