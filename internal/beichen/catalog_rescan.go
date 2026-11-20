package beichen

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Service) RescanCatalog(ctx context.Context, provider, credential uuid.UUID) error {
	if provider == uuid.Nil || credential == uuid.Nil {
		return invalid("需要有效的供应商和渠道 ID")
	}
	if s.db == nil || s.crypto == nil {
		return unavailable("北辰同步未配置", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, jobTimeout+5*time.Second)
	defer cancel()
	var found uuid.UUID
	err := s.db.QueryRowContext(ctx, `
SELECT c.id FROM provider_credentials c JOIN providers p ON p.id=c.provider_id
WHERE p.id=$1 AND c.id=$2 AND p.status='active' AND c.status='active' AND `+beichenProvider,
		provider, credential).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "找不到该北辰供应商下已启用的渠道 Key")
	}
	if err != nil {
		return unavailable("读取北辰渠道失败", err)
	}
	busy, err := s.runJob(ctx, syncJob{provider: provider, credential: credential, kind: "catalog"}, true)
	if err != nil {
		return unavailable("所选渠道目录重扫未完成，请查看渠道状态后重试", err)
	}
	if busy {
		return domain.NewError(domain.CodeInvalidTransition, "所选渠道正在同步，请稍后重扫")
	}
	return nil
}
