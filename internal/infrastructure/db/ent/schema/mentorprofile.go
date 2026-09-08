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

type MentorProfile struct{ ent.Schema }

func (MentorProfile) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "mentor_profiles"}}
}

func (MentorProfile) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (MentorProfile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable().Unique(),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.UUID("granted_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("granted_at").Optional().Nillable(),
	}
}

func (MentorProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("mentor_profile").
			Field("user_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("grantor", User.Type).
			Ref("granted_mentor_profiles").
			Field("granted_by").
			Unique(),
	}
}

func (MentorProfile) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("granted_by"),
	}
}
