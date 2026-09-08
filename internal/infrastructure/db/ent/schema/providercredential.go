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

type ProviderCredential struct {
	ent.Schema
}

func (ProviderCredential) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "provider_credentials"}}
}

func (ProviderCredential) Mixin() []ent.Mixin {
	return []ent.Mixin{UUIDMixin{}, TimeMixin{}}
}

func (ProviderCredential) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("provider_id", uuid.UUID{}).Immutable(),
		field.String("label").
			NotEmpty().
			MaxLen(128).
			SchemaType(map[string]string{dialect.Postgres: "citext"}),
		field.String("group_hint").Optional().Nillable().MaxLen(128),
		field.Bytes("credential_ciphertext").NotEmpty().Sensitive(),
		field.Enum("status").Values("active", "inactive").Default("active"),
	}
}

func (ProviderCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("provider", Provider.Type).
			Ref("credentials").
			Field("provider_id").
			Unique().
			Required().
			Immutable().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("model_catalog_entries", ProviderModelCatalog.Type),
		edge.To("model_bindings", ModelBinding.Type),
	}
}

func (ProviderCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_id", "label").Unique(),
		index.Fields("provider_id", "status"),
	}
}
