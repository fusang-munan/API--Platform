package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	beichenCatalogAuthKey   = "beichen_catalog_auth/"
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
	// The independent daily worker shares the catalog row's Retry-After backoff
	// with scheduled billing jobs. A manual rescan remains an explicit override.
	var nextAttempt sql.NullTime
	err := r.DB.QueryRowContext(ctx, `
SELECT next_attempt FROM beichen_datasets WHERE credential_id=$1 AND kind='catalog'`, row.ID).Scan(&nextAttempt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && nextAttempt.Valid && nextAttempt.Time.After(r.now()) {
		return false, nil
	}
	lease, err := AcquireBeichenDatasetLease(ctx, r.DB, row.ID, "billing", true)
	if lease == uuid.Nil || err != nil {
		return false, err
	}
	var snapshot BeichenModelSnapshot
	secret, fetchErr := r.Decrypt(row.CredentialCiphertext)
	if fetchErr == nil {
		read, cancel := context.WithTimeout(ctx, BeichenScanTimeout)
		snapshot, fetchErr = FetchBeichenModelSnapshot(read, secret, nil)
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
		outcome, err = SaveBeichenKeyScan(save, tx, row.ProviderID, row.ID, row.UpdatedAt, r.now().UTC(), snapshot, fetchErr)
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

type beichenCatalogRemovals struct {
	Revision time.Time      `json:"revision"`
	Missing  map[string]int `json:"missing"`
}

// SaveBeichenKeyScan applies positive listings and confirms removals under the
// billing lease. The first error describes the observation; the second
// requires a rollback.
func SaveBeichenKeyScan(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, revision, at time.Time,
	snapshot BeichenModelSnapshot, fetchErr error) (error, error) {
	if fetchErr != nil {
		if errors.Is(fetchErr, ErrBeichenKeyGroupNotAllowed) {
			if err := saveBeichenGroupDenial(ctx, tx, provider, credential, revision, "", true); err != nil {
				return nil, err
			}
		} else if beichenCatalogAuthFailure(fetchErr) {
			// A definitive 401/403 is evidence about this Key revision. Later
			// transient failures cannot disprove it; only a successful scan can.
			if err := saveBeichenCatalogAuth(ctx, tx, credential, revision, true); err != nil {
				return nil, err
			}
		}
		return fetchErr, recordBeichenCatalogFailure(ctx, tx, credential, fetchErr)
	}
	if err := saveBeichenGroupDenial(ctx, tx, provider, credential, revision, "", false); err != nil {
		return nil, err
	}
	if err := saveBeichenCatalogAuth(ctx, tx, credential, revision, false); err != nil {
		return nil, err
	}
	if err := confirmBeichenCatalogRemovals(ctx, tx, provider, credential, revision, at, snapshot); err != nil {
		return nil, err
	}
	if _, err := upsertCredentialEntries(ctx, tx, provider, credential, snapshot.Entries, at); err != nil {
		return nil, err
	}
	// Website discovery has its own snapshot. Remove only duplicate legacy
	// website rows after ensuring no active binding still needs them.
	if _, err := tx.ExecContext(ctx, `
UPDATE provider_model_catalog k SET status='inactive',synced_at=$4,updated_at=$4
WHERE k.provider_id=$1 AND k.credential_id=$2 AND k.status='active' AND k.catalog_source='website'
  AND EXISTS (SELECT 1 FROM beichen_website_catalogs w,unnest(w.models) models(model_id)
    WHERE w.credential_id=k.credential_id AND w.credential_revision=$3 AND btrim(models.model_id)::citext=k.model_id)
  AND NOT EXISTS (SELECT 1 FROM model_bindings b WHERE b.provider_id=k.provider_id AND b.status='active'
    AND (b.credential_id=k.credential_id OR b.credential_id IS NULL) AND btrim(b.upstream_model_name)::citext=k.model_id)`,
		provider, credential, revision, at); err != nil {
		return nil, err
	}
	return nil, recordBeichenCatalogSuccess(ctx, tx, credential)
}

func recordBeichenCatalogFailure(ctx context.Context, tx *sql.Tx, credential uuid.UUID, fetchErr error) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id,kind,last_error) VALUES ($1,'catalog',$2)
ON CONFLICT (credential_id,kind) DO UPDATE SET last_error=EXCLUDED.last_error`, credential, fetchErr.Error())
	if err != nil {
		return err
	}
	retryAt := BeichenRetryAt(fetchErr)
	if retryAt.IsZero() {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id,kind,next_attempt) VALUES ($1,'catalog',$2)
ON CONFLICT (credential_id,kind) DO UPDATE SET next_attempt=GREATEST(
  COALESCE(beichen_datasets.next_attempt,'-infinity'::timestamptz),EXCLUDED.next_attempt)`, credential, retryAt)
	return err
}

func recordBeichenCatalogSuccess(ctx context.Context, tx *sql.Tx, credential uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id,kind,last_success,next_attempt) VALUES ($1,'catalog',clock_timestamp(),clock_timestamp())
ON CONFLICT (credential_id,kind) DO UPDATE SET last_success=EXCLUDED.last_success,last_error=NULL,
  next_attempt=LEAST(COALESCE(beichen_datasets.next_attempt,clock_timestamp()),clock_timestamp())`, credential)
	return err
}

func beichenCatalogAuthFailure(err error) bool {
	return errors.Is(err, BeichenHTTPStatus(401)) || errors.Is(err, BeichenHTTPStatus(403))
}

type beichenCatalogAuth struct {
	Revision time.Time `json:"credential_revision"`
}

// A definitive authentication failure belongs to the current credential
// revision. A later successful scan proves the Key can read its directory.
func saveBeichenCatalogAuth(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision time.Time, failed bool) error {
	name := beichenCatalogAuthKey + credential.String()
	if !failed {
		_, err := tx.ExecContext(ctx, "DELETE FROM maintenance_state WHERE name=$1", name)
		return err
	}
	raw, err := json.Marshal(beichenCatalogAuth{Revision: revision})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO maintenance_state (id,name,last_error,created_at,updated_at)
VALUES ($1,$2,$3,clock_timestamp(),clock_timestamp())
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,updated_at=EXCLUDED.updated_at`,
		uuid.Must(uuid.NewV7()), name, string(raw))
	return err
}

type beichenGroupDenial struct {
	Code        string          `json:"code"`
	Revision    time.Time       `json:"credential_revision"`
	Catalog     *bool           `json:"catalog_denied"`
	ProbeRoutes map[string]bool `json:"probe_routes,omitempty"`
	GroupID     *int64          `json:"group_id,omitempty"`
}

// Catalog and probe observations can clear only the denial they can recheck.
// The caller holds the credential row lock for the observed revision.
func SaveBeichenProbeGroupDenial(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision time.Time,
	model, protocol string, denied bool) error {
	provider, err := beichenCredentialProvider(ctx, tx, credential)
	if err != nil {
		return err
	}
	return saveBeichenGroupDenial(ctx, tx, provider, credential, revision, protocol+"/"+model, denied)
}

func beichenCredentialProvider(ctx context.Context, tx *sql.Tx, credential uuid.UUID) (uuid.UUID, error) {
	var provider uuid.UUID
	err := tx.QueryRowContext(ctx, "SELECT provider_id FROM provider_credentials WHERE id=$1", credential).Scan(&provider)
	return provider, err
}

func saveBeichenGroupDenial(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID, revision time.Time, route string, denied bool) error {
	name := beichenGroupDenialKey + credential.String()
	var saved sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT last_error FROM maintenance_state WHERE name=$1", name).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var state beichenGroupDenial
	current := json.Unmarshal([]byte(saved.String), &state) == nil && state.Revision.Equal(revision)
	groupID, err := beichenKnownGroup(ctx, tx, credential)
	if err != nil {
		return err
	}
	reset := !current || !sameBeichenGroup(state.GroupID, groupID)
	if reset {
		state = beichenGroupDenial{Code: "GROUP_NOT_ALLOWED", Revision: revision, GroupID: groupID}
	} else {
		state.GroupID = groupID
	}
	// Old denial markers predate probes and represent a denied catalog read.
	catalogDenied := !reset && current && (state.Catalog == nil || *state.Catalog)
	if route == "" {
		if !denied && !catalogDenied && !reset {
			return nil
		}
		catalogDenied = denied
	} else if denied {
		if state.ProbeRoutes == nil {
			state.ProbeRoutes = map[string]bool{}
		}
		state.ProbeRoutes[route] = true
	} else if state.ProbeRoutes[route] {
		delete(state.ProbeRoutes, route)
	} else if !reset {
		return nil
	}
	if !catalogDenied && len(state.ProbeRoutes) == 0 {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM maintenance_state WHERE name=$1", name)
		return err
	}
	state.Catalog = &catalogDenied
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO maintenance_state (id,name,last_error,created_at,updated_at)
VALUES ($1,$2,$3,clock_timestamp(),clock_timestamp())
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,updated_at=EXCLUDED.updated_at`, uuid.Must(uuid.NewV7()), name, string(raw))
	return err
}

func beichenKnownGroup(ctx context.Context, tx *sql.Tx, credential uuid.UUID) (*int64, error) {
	var group sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT e.group_id FROM (`+BeichenKeyEvidenceSQL+` WHERE c.id=$1) e`, credential).Scan(&group)
	if err != nil {
		return nil, err
	}
	if !group.Valid {
		return nil, nil
	}
	return &group.Int64, nil
}

func sameBeichenGroup(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func confirmBeichenCatalogRemovals(ctx context.Context, tx *sql.Tx, provider, credential uuid.UUID,
	revision, at time.Time, snapshot BeichenModelSnapshot) error {
	stateName := "beichen_catalog_shrink/" + credential.String()
	var saved sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT last_error FROM maintenance_state WHERE name=$1", stateName).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previous beichenCatalogRemovals
	if json.Unmarshal([]byte(saved.String), &previous) != nil || !previous.Revision.Equal(revision) {
		previous.Missing = nil
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `
SELECT COALESCE(json_agg(lower(btrim(model_id::text))), '[]')
FROM provider_model_catalog WHERE provider_id=$1 AND credential_id=$2 AND status='active'`,
		provider, credential).Scan(&raw)
	if err != nil {
		return err
	}
	var active []string
	if err := json.Unmarshal(raw, &active); err != nil {
		return err
	}
	seen := upstreamNameSet(snapshot.Entries)
	pending := beichenCatalogRemovals{Revision: revision, Missing: map[string]int{}}
	var confirmed []string
	for _, model := range active {
		if _, present := seen[model]; present {
			continue
		}
		rounds := previous.Missing[model]
		if snapshot.Complete {
			rounds++
		}
		if rounds >= BeichenShrinkRounds {
			confirmed = append(confirmed, model)
		} else if rounds > 0 {
			pending.Missing[model] = rounds
		}
	}
	if len(confirmed) > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_model_catalog SET status='inactive',synced_at=$3,updated_at=$3
WHERE provider_id=$1 AND credential_id=$2 AND status='active' AND lower(btrim(model_id::text))=ANY($4::text[])`,
			provider, credential, at, confirmed); err != nil {
			return err
		}
	}
	if len(pending.Missing) == 0 {
		_, err := tx.ExecContext(ctx, "DELETE FROM maintenance_state WHERE name=$1", stateName)
		return err
	}
	raw, err = json.Marshal(pending)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO maintenance_state (id,name,last_error,created_at,updated_at)
VALUES ($1,$2,$3,clock_timestamp(),clock_timestamp())
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,updated_at=EXCLUDED.updated_at`,
		uuid.Must(uuid.NewV7()), stateName, string(raw))
	return err
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
