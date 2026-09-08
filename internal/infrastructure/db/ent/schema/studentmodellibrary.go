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

type StudentModelLibrary struct{ ent.Schema }

func (StudentModelLibrary) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "student_model_libraries"}}
}

func (StudentModelLibrary) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (StudentModelLibrary) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.UUID("model_id", uuid.UUID{}).Immutable(),
	}
}

func (StudentModelLibrary) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("model_library_entries").
			Field("user_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("model", Model.Type).
			Ref("student_library_entries").
			Field("model_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (StudentModelLibrary) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "model_id").Unique(),
		index.Fields("user_id"),
	}
}
