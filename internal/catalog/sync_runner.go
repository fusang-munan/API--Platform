package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/provider"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/providercredential"
)

const (
	BeichenScanLeaseSeconds = 90
	BeichenScanTimeout      = 25 * time.Second
	BeichenShrinkRounds     = 3
	beichenGroupDenialKey   = "beichen_group_denial/"
)

type ProviderSyncRunner struct {
	DB      *sql.DB
	Ent     *ent.Client
	Decrypt func([]byte) (string, error)
	Now     func() time.Time
}

type activeCredentialRow struct {
	ID                   uuid.UUID
	ProviderID           uuid.UUID
	BaseURL              string
	Label                string
	GroupHint            sql.NullString
	CredentialCiphertext []byte
	UpdatedAt            time.Time
}

func (r *ProviderSyncRunner) SyncActiveProviders(ctx context.Context) (int, error) {
	if r.DB == nil || r.Ent == nil || r.Decrypt == nil {
		return 0, nil
	}
	now := r.now().UTC()
	rows, err := r.DB.QueryContext(ctx, `
SELECT pc.id, pc.provider_id, p.base_url, pc.label, pc.group_hint, pc.credential_ciphertext, pc.updated_at
FROM provider_credentials pc
JOIN providers p ON p.id = pc.provider_id
WHERE p.status='active' AND pc.status='active'
ORDER BY lower(p.name), p.name, lower(pc.label), pc.label`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var credentials []activeCredentialRow
	for rows.Next() {
		var row activeCredentialRow
		if err := rows.Scan(&row.ID, &row.ProviderID, &row.BaseURL, &row.Label, &row.GroupHint, &row.CredentialCiphertext, &row.UpdatedAt); err != nil {
			return 0, err
		}
		credentials = append(credentials, row)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	synced := 0
	var failures []error
	syncer := &ProviderCatalogSyncer{DB: r.DB}
	for _, row := range credentials {
		if IsBeichenBaseURL(row.BaseURL) {
			written, err := r.syncBeichenCredential(ctx, row)
			if err != nil {
				failures = append(failures, fmt.Errorf("北辰渠道 %s：%w", row.ID, err))
			}
			if written {
				synced++
			}
			continue
		}
		credential, err := r.Decrypt(row.CredentialCiphertext)
		if err != nil {
			continue
		}
		entries, err := FetchProviderModels(ctx, row.BaseURL, credential, nil)
		if err != nil {
			continue
		}
		var groupHint *string
		if row.GroupHint.Valid {
			hint := row.GroupHint.String
			groupHint = &hint
		}
		entries = FilterProviderEntriesByCredential(row.Label, groupHint, row.BaseURL, entries)
		if _, err := syncer.UpsertCredentialEntries(ctx, row.ProviderID, row.ID, entries, now, nil); err != nil {
			return synced, errors.Join(append(failures, err)...)
		}
		synced++
	}
	return synced, errors.Join(failures...)
}

func (r *ProviderSyncRunner) syncBeichenCredential(ctx context.Context, row activeCredentialRow) (bool, error) {
	lease, err := AcquireBeichenDatasetLease(ctx, r.DB, row.ID, "billing", true)
	if lease == uuid.Nil || err != nil {
		return false, err
	}
	var entries []ProviderEntry
	secret, fetchErr := r.Decrypt(row.CredentialCiphertext)
	if fetchErr == nil {
		read, cancel := context.WithTimeout(ctx, BeichenScanTimeout)
		entries, fetchErr = FetchProviderModels(read, row.BaseURL, secret, nil)
		cancel()
	} else {
		fetchErr = errors.New("北辰凭据解密失败")
	}

	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(save, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := LockBeichenScan(save, tx, row.ProviderID, row.ID, "billing", lease, row.UpdatedAt)
	if err != nil {
		return false, err
	}
	var outcome error
	if current {
		outcome, err = SaveBeichenKeyScan(save, tx, row.ProviderID, row.ID, row.UpdatedAt, r.now().UTC(), entries, fetchErr)
	} else {
		outcome = errors.New("同步期间北辰凭据或供应商已变化")
	}
	if err == nil {
		err = ReleaseBeichenDatasetLease(save, tx, row.ID, "billing")
	}
	if err == nil {
		err = tx.Commit()
	}
	return err == nil && outcome == nil, errors.Join(err, outcome)
}

// The lease UUID identifies the scan round across both processes. An expired
// UUID cannot commit, even when its Key revision happens to be unchanged.
func AcquireBeichenDatasetLease(ctx context.Context, db *sql.DB, credential uuid.UUID, kind string, manual bool) (uuid.UUID, error) {
	lease := uuid.New()
	err := db.QueryRowContext(ctx, `
INSERT INTO beichen_datasets (credential_id, kind, lease_id, lease_until)
VALUES ($1, $2, $3, clock_timestamp() + $4::int * interval '1 second')
ON CONFLICT (credential_id, kind) DO UPDATE SET lease_id = EXCLUDED.lease_id, lease_until = EXCLUDED.lease_until
WHERE (beichen_datasets.lease_until IS NULL OR beichen_datasets.lease_until <= clock_timestamp())
  AND ($5::boolean OR beichen_datasets.next_attempt <= clock_timestamp())
RETURNING lease_id`, credential, kind, lease, BeichenScanLeaseSeconds, manual).Scan(&lease)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil
	}
	return lease, err
}

const beichenRebindLockKey = `hashtextextended('beichen_datasets'::regclass::oid::text || '/rebind/' || $1::uuid::text, 0)`

// Confirmations take the exclusive side of the lock used by evidence writers.
func TryLockBeichenRebind(ctx context.Context, tx *sql.Tx, provider uuid.UUID) (bool, error) {
	var locked bool
	err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(`+beichenRebindLockKey+`)`, provider).Scan(&locked)
	return locked, err
}

func LockBeichenScan(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, kind string, lease uuid.UUID, revision time.Time) (bool, error) {
	if kind == "billing" || kind == "prices" || kind == "key_usage" {
		// Take the advisory lock before the provider and credential row locks.
		// Shared holders can still persist observations for different Keys concurrently.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(`+beichenRebindLockKey+`)`, provider); err != nil {
			return false, err
		}
	}
	var current time.Time
	err := tx.QueryRowContext(ctx, `
SELECT c.updated_at FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE p.id=$1 AND c.id=$2 AND c.status='active' AND p.status='active'
  AND lower(btrim(p.base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/',
    'https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')
FOR SHARE OF p FOR NO KEY UPDATE OF c`, provider, credential).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	validRevision := err == nil && current.Equal(revision)
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `
SELECT lease_id = $3 AND lease_until > clock_timestamp() FROM beichen_datasets
WHERE credential_id = $1 AND kind = $2 AND lease_id IS NOT NULL FOR UPDATE`, credential, kind, lease).Scan(&ownsLease)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if !ownsLease {
		return false, errors.New("北辰同步任务租约已失效")
	}
	return validRevision, nil
}

// The caller must hold the lease row lock returned by LockBeichenScan.
func ReleaseBeichenDatasetLease(ctx context.Context, tx *sql.Tx, credential uuid.UUID, kind string) error {
	_, err := tx.ExecContext(ctx, `UPDATE beichen_datasets SET lease_id=NULL,lease_until=NULL
WHERE credential_id=$1 AND kind=$2`, credential, kind)
	return err
}

type beichenShrinkCandidate struct {
	Revision    time.Time `json:"revision"`
	Fingerprint string    `json:"fingerprint"`
	Rounds      int       `json:"rounds"`
}

// SaveBeichenKeyScan stages a sharp drop under the billing lease. The first
// error describes the observed result; the second requires a rollback.
func SaveBeichenKeyScan(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, revision, at time.Time,
	entries []ProviderEntry, fetchErr error) (error, error) {
	stateName := "beichen_catalog_shrink/" + credential.String()
	var candidate *beichenShrinkCandidate
	observationErr := fetchErr
	if fetchErr == nil {
		// A successful read establishes access even when its model coverage
		// needs more observations before we can replace the saved directory.
		if _, err := tx.ExecContext(ctx, "DELETE FROM maintenance_state WHERE name=$1", beichenGroupDenialKey+credential.String()); err != nil {
			return nil, err
		}
		var previousCount int
		err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM provider_model_catalog
WHERE credential_id=$1 AND status='active' AND catalog_source='key'`, credential).Scan(&previousCount)
		if err != nil {
			return nil, err
		}
		if BeichenSnapshotShrank(previousCount, len(entries)) {
			var priorText sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT last_error FROM maintenance_state WHERE name=$1", stateName).Scan(&priorText)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			var prior beichenShrinkCandidate
			_ = json.Unmarshal([]byte(priorText.String), &prior)
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, strings.ToLower(entry.ID))
			}
			slices.Sort(names)
			raw, _ := json.Marshal(names)
			fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
			rounds := 1
			if prior.Revision.Equal(revision) && prior.Fingerprint == fingerprint && prior.Rounds > 0 && prior.Rounds < BeichenShrinkRounds {
				rounds = prior.Rounds + 1
			}
			if rounds < BeichenShrinkRounds {
				candidate = &beichenShrinkCandidate{Revision: revision, Fingerprint: fingerprint, Rounds: rounds}
				observationErr = fmt.Errorf("北辰 Key 模型目录待核验：由 %d 个降至 %d 个，连续一致 %d/%d，沿用上次目录",
					previousCount, len(entries), rounds, BeichenShrinkRounds)
			}
		}
	}
	if candidate == nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM maintenance_state WHERE name=$1", stateName); err != nil {
			return nil, err
		}
	} else {
		raw, err := json.Marshal(candidate)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO maintenance_state (id,name,last_error,created_at,updated_at)
VALUES ($1,$2,$3,clock_timestamp(),clock_timestamp())
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,updated_at=EXCLUDED.updated_at`,
			uuid.Must(uuid.NewV7()), stateName, string(raw)); err != nil {
			return nil, err
		}
	}
	if observationErr != nil {
		if errors.Is(fetchErr, ErrBeichenKeyGroupNotAllowed) {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO maintenance_state (id,name,last_error,created_at,updated_at)
VALUES ($1,$2,jsonb_build_object('code','GROUP_NOT_ALLOWED','credential_revision',$3::timestamptz)::text,
  statement_timestamp(),statement_timestamp())
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,updated_at=EXCLUDED.updated_at`,
				uuid.Must(uuid.NewV7()), beichenGroupDenialKey+credential.String(), revision); err != nil {
				return nil, err
			}
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id,kind,last_error) VALUES ($1,'catalog',$2)
ON CONFLICT (credential_id,kind) DO UPDATE SET last_error=EXCLUDED.last_error`, credential, observationErr.Error())
		return observationErr, err
	}
	if _, err := ReplaceCredentialEntries(ctx, tx, provider, credential, entries, at); err != nil {
		return nil, err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id,kind,last_success) VALUES ($1,'catalog',clock_timestamp())
ON CONFLICT (credential_id,kind) DO UPDATE SET last_success=EXCLUDED.last_success,last_error=NULL`, credential)
	return nil, err
}

func BeichenSnapshotShrank(previous, current int) bool {
	return previous > 0 && current <= previous/2
}

func (r *ProviderSyncRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func ActiveCredentials(ctx context.Context, client *ent.Client, providerID *uuid.UUID) ([]*ent.ProviderCredential, error) {
	query := client.ProviderCredential.Query().
		Where(providercredential.StatusEQ(providercredential.StatusActive)).
		Where(providercredential.HasProviderWith(provider.StatusEQ(provider.StatusActive))).
		WithProvider().
		Order(
			ent.Asc(providercredential.FieldLabel),
			ent.Asc(providercredential.FieldID),
		)
	if providerID != nil {
		query = query.Where(providercredential.ProviderIDEQ(*providerID))
	}
	return query.All(ctx)
}
