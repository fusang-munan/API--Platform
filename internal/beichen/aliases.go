package beichen

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type ModelAlias struct {
	CredentialID  *uuid.UUID `json:"credential_id"`
	UpstreamModel string     `json:"upstream_model"`
	SchoolModel   string     `json:"school_model"`
}

// The fact row a retains the raw upstream name for price and receipt lookups.
// Use the alias only when grouping or displaying the two sides of the ledger.
const joinModelAliasSQL = `
LEFT JOIN LATERAL (
  SELECT entry.school_model FROM upstream_model_aliases entry
  WHERE entry.provider_id = $1 AND (entry.credential_id = a.credential_id OR entry.credential_id IS NULL)
    AND lower(entry.upstream_model) = lower(a.model)
  ORDER BY entry.credential_id NULLS LAST, entry.created_at DESC, entry.id DESC
  LIMIT 1
) alias ON true`

func (s *Service) ModelAliases(ctx context.Context, provider uuid.UUID) ([]ModelAlias, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, unavailable("读取北辰模型别名失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT ON (credential_id, lower(upstream_model)) credential_id, upstream_model, school_model
FROM upstream_model_aliases WHERE provider_id = $1
ORDER BY credential_id NULLS FIRST, lower(upstream_model), created_at DESC, id DESC`, provider)
	if err != nil {
		return nil, unavailable("读取北辰模型别名失败", err)
	}
	defer rows.Close()
	aliases := []ModelAlias{}
	for rows.Next() {
		var item ModelAlias
		if err := rows.Scan(&item.CredentialID, &item.UpstreamModel, &item.SchoolModel); err != nil {
			return nil, unavailable("读取北辰模型别名失败", err)
		}
		aliases = append(aliases, item)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable("读取北辰模型别名失败", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, unavailable("读取北辰模型别名失败", err)
	}
	return aliases, nil
}

func (s *Service) PutModelAlias(ctx context.Context, provider, actor uuid.UUID, credential *uuid.UUID, upstream string, school *string) error {
	if provider == uuid.Nil || actor == uuid.Nil || (credential != nil && *credential == uuid.Nil) ||
		!validModel(upstream) || (school != nil && !validModel(*school)) {
		return invalid("请填写有效的渠道和模型名称")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable("保存北辰模型别名失败", err)
	}
	defer tx.Rollback()
	if err := lockAliasProvider(ctx, tx, provider); err != nil {
		return err
	}
	if credential != nil {
		if err := s.lockCredential(ctx, tx, provider, *credential); err != nil {
			return aliasWriteError(err)
		}
	}
	var target *string
	if school != nil {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT model_id::text FROM models WHERE model_id=$1::citext FOR KEY SHARE`, *school).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeNotFound, "校内模型不存在")
		}
		if err != nil {
			return aliasWriteError(err)
		}
		target = &name
	}
	// Clean up case variants, retaining the row identity on ordinary updates.
	_, err = tx.ExecContext(ctx, `
DELETE FROM upstream_model_aliases WHERE provider_id=$1 AND credential_id IS NOT DISTINCT FROM $2::uuid
  AND lower(upstream_model)=lower($3) AND ($4::text IS NULL OR upstream_model<>lower($3))`,
		provider, credential, upstream, target)
	if err == nil && target != nil {
		_, err = tx.ExecContext(ctx, `
INSERT INTO upstream_model_aliases (provider_id, credential_id, upstream_model, school_model, created_by)
VALUES ($1,$2,lower($3),$4,$5)
ON CONFLICT (provider_id, credential_id, upstream_model) DO UPDATE SET school_model=EXCLUDED.school_model
WHERE upstream_model_aliases.school_model IS DISTINCT FROM EXCLUDED.school_model`, provider, credential, upstream, target, actor)
	}
	if err == nil {
		err = tx.Commit()
	}
	return aliasWriteError(err)
}

func aliasWriteError(err error) error {
	var appErr *domain.Error
	if err != nil && !errors.As(err, &appErr) {
		return unavailable("保存北辰模型别名失败", err)
	}
	return err
}

// Alias writers and rebind serialize the model mapping with catalog sync,
// which holds FOR SHARE on the provider before persisting a Key's snapshot.
func lockAliasProvider(ctx context.Context, tx *sql.Tx, provider uuid.UUID) error {
	err := tx.QueryRowContext(ctx, `SELECT p.id FROM providers p WHERE p.id=$1 AND `+beichenProvider+
		` FOR NO KEY UPDATE`, provider).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "北辰供应商不存在")
	}
	if err != nil {
		return unavailable("锁定北辰供应商失败", err)
	}
	return nil
}
