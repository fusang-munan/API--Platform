package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type ProviderModelCatalog struct{ ent.Schema }

func (ProviderModelCatalog) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "provider_model_catalog"}}
}

func (ProviderModelCatalog) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (ProviderModelCatalog) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("provider_id", uuid.UUID{}).Immutable(),
		field.UUID("credential_id", uuid.UUID{}).Immutable(),
		field.String("model_id").
			NotEmpty().
			MaxLen(256).
			SchemaType(map[string]string{dialect.Postgres: "citext"}),
		field.String("display_name").Optional().Nillable().MaxLen(256),
		field.String("description").Optional().Nillable().MaxLen(4096),
		field.String("owned_by").Optional().Nillable().MaxLen(256),
		field.String("upstream_object").Optional().Nillable().MaxLen(64),
		field.String("upstream_created_at").Optional().Nillable().MaxLen(64),
		field.JSON("raw_entry", map[string]any{}).Default(map[string]any{}),
		field.Enum("status").Values("active", "inactive").Default("active"),
		field.Time("last_seen_at"),
		field.Time("synced_at"),
	}
}

func (ProviderModelCatalog) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("provider", Provider.Type).
			Ref("model_catalog_entries").
			Field("provider_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("credential", ProviderCredential.Type).
			Ref("model_catalog_entries").
			Field("credential_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (ProviderModelCatalog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("credential_id", "model_id").Unique(),
		index.Fields("provider_id", "status", "model_id"),
	}
}
