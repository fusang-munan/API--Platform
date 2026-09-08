package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type StudentProfile struct{ ent.Schema }

func (StudentProfile) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "student_profiles"}}
}

func (StudentProfile) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (StudentProfile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable().Unique(),
		field.Enum("status").Values("active", "disabled").Default("active"),
	}
}

func (StudentProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("student_profile").
			Field("user_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (StudentProfile) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
