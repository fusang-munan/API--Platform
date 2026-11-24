package beichen

import (
	"context"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func (s *Service) PendingAudit(ctx context.Context, providerID uuid.UUID, apply bool, limit, offset int, token string) (usage.PendingBacklogReport, error) {
	if err := checkProvider(ctx, s.db, providerID); err != nil {
		return usage.PendingBacklogReport{}, err
	}
	return usage.PendingAuditBacklog(ctx, s.db, providerID, apply, limit, offset, token)
}

func (s *Service) ResolveTerminalPending(ctx context.Context, providerID uuid.UUID) (usage.TerminalPendingResult, error) {
	if err := checkProvider(ctx, s.db, providerID); err != nil {
		return usage.TerminalPendingResult{}, err
	}
	return usage.ResolveProviderTerminalPending(ctx, s.db, providerID)
}
