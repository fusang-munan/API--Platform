package controlplane

import (
	"context"
)

type ModelCatalogCandidate struct {
	ModelID           string         `json:"model_id"`
	DisplayName       *string        `json:"display_name,omitempty"`
	Description       *string        `json:"description,omitempty"`
	OwnedBy           *string        `json:"owned_by,omitempty"`
	UpstreamObject    *string        `json:"upstream_object,omitempty"`
	UpstreamCreatedAt *string        `json:"upstream_created_at,omitempty"`
	CredentialID      string         `json:"credential_id,omitempty"`
	CredentialLabel   string         `json:"credential_label,omitempty"`
	ProviderName      string         `json:"provider_name,omitempty"`
	CreditMultiplier  *string        `json:"credit_multiplier,omitempty"`
	RawEntry          map[string]any `json:"raw_entry"`
}

type ModelCatalogPage struct {
	Items    []ModelCatalogCandidate `json:"items"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Total    int                     `json:"total"`
}

func (s *Service) ModelCatalogCandidates(ctx context.Context, query string, page, pageSize int) (ModelCatalogPage, error) {
	active, err := s.ActiveProvider(ctx)
	if err != nil {
		return ModelCatalogPage{}, err
	}
	return s.ProviderCatalogCandidates(ctx, active.ID, query, page, pageSize)
}
