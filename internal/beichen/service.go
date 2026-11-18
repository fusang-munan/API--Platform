package beichen

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

const (
	origin          = "https://api.beichenapi.com"
	dayLayout       = "2006-01-02"
	maxRate   int64 = 1_000_000_000_000
	// The predicate only matches spellings which the fixed-origin client can handle.
	beichenProvider = "lower(btrim(p.base_url)) IN ('https://api.beichenapi.com','https://api.beichenapi.com/','https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/')"
)

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

type Rates struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

type PriceInput struct {
	Mode  string `json:"mode"`
	Rates *Rates `json:"rates,omitempty"`
}

type Service struct {
	db          *sql.DB
	crypto      *security.Manager
	client      *http.Client
	now         func() time.Time
	panelToken  string
	panelGroups map[uuid.UUID]int64
}

func New(db *sql.DB, crypto *security.Manager) *Service {
	return &Service{db: db, crypto: crypto, client: newClient(), now: time.Now}
}

func invalid(message string) error {
	return domain.NewError(domain.CodeValidation, message)
}

func unavailable(message string, err error) error {
	return domain.WrapError(domain.CodeDependencyUnavailable, message, err)
}

func validModel(model string) bool {
	return model != "" && strings.TrimSpace(model) == model && utf8.ValidString(model) &&
		utf8.RuneCountInString(model) <= 256 && strings.IndexFunc(model, unicode.IsControl) == -1
}

func validRates(r *Rates) bool {
	if r == nil || r.Input == nil || r.Output == nil {
		return false
	}
	for _, rate := range []*int64{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
		if rate != nil && (*rate < 0 || *rate > maxRate) {
			return false
		}
	}
	return true
}

func dayStart(day string) (time.Time, error) {
	t, err := time.ParseInLocation(dayLayout, day, beijing)
	if err != nil || t.Format(dayLayout) != day || t.Year() < 2000 {
		return time.Time{}, invalid("日期须为 YYYY-MM-DD，年份须为 2000 年或以后")
	}
	return t, nil
}

const lockCredentialSQL = `
SELECT c.id FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
WHERE p.id = $1 AND c.id = $2 AND ` + beichenProvider + `
FOR SHARE OF p FOR NO KEY UPDATE OF c`

func (s *Service) lockCredential(ctx context.Context, tx *sql.Tx, providerID, credentialID uuid.UUID) error {
	err := tx.QueryRowContext(ctx, lockCredentialSQL, providerID, credentialID).Scan(&credentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "北辰凭据不存在或不属于该供应商")
	}
	return err
}

func (s *Service) write(ctx context.Context, providerID, credentialID uuid.UUID, action func(*sql.Tx) error) error {
	if providerID == uuid.Nil || credentialID == uuid.Nil {
		return invalid("需要供应商和凭据 ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable("开启北辰数据事务失败", err)
	}
	defer tx.Rollback()
	if err = s.lockCredential(ctx, tx, providerID, credentialID); err == nil {
		err = action(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	var appErr *domain.Error
	if err != nil && !errors.As(err, &appErr) {
		return unavailable("保存北辰数据失败", err)
	}
	return err
}

func (s *Service) SetPrice(ctx context.Context, providerID, credentialID, actorID uuid.UUID, model string, input PriceInput) error {
	if actorID == uuid.Nil || !validModel(model) {
		return invalid("需要有效的教师 ID 和模型名")
	}
	switch input.Mode {
	case "follow":
		if input.Rates != nil {
			return invalid("跟随模式无须提交校内单价")
		}
	case "manual":
		if !validRates(input.Rates) {
			return invalid("手动单价必须包含非负整数输入价和输出价，单价上限为 1000000000000")
		}
	default:
		return invalid("价格模式须为 follow 或 manual")
	}
	return s.write(ctx, providerID, credentialID, func(tx *sql.Tx) error {
		var exists bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM provider_model_catalog WHERE credential_id = $1 AND model_id::text = $2
			UNION ALL SELECT 1 FROM beichen_price_versions WHERE credential_id = $1 AND model = $2
			UNION ALL SELECT 1 FROM beichen_daily_usage WHERE credential_id = $1 AND model = $2
		)`, credentialID, model).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return domain.NewError(domain.CodeNotFound, "该北辰凭据下没有此模型")
		}
		var rates Rates
		if input.Rates != nil {
			rates = *input.Rates
		}
		effectiveAt, err := priceTimestamp(ctx, tx)
		if err != nil {
			return err
		}
		return savePrice(ctx, tx, credentialID, model, "school", input.Mode, "teacher", &actorID, rates, effectiveAt)
	})
}

func priceTimestamp(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	// Sample once after locking the credential, so overlapping writes are
	// versioned in lock order and all rows in this batch take effect together.
	var effectiveAt time.Time
	err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&effectiveAt)
	return effectiveAt, err
}

func savePrice(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, model, track, basis, source string, actor *uuid.UUID, rates Rates, effectiveAt time.Time) error {
	// The credential row is locked by the caller. Identical observations do not
	// generate versions, but a teacher or source change is retained in the history.
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_price_versions (credential_id, model, track, basis, source, actor_id, input_rate, output_rate, cache_read_rate, cache_write_rate, effective_at)
SELECT $1::uuid, $2::text, $3::text, $4::text, $5::text, $6::uuid,
  $7::bigint, $8::bigint, $9::bigint, $10::bigint, $11::timestamptz
WHERE NOT EXISTS (
  SELECT 1 FROM (
    SELECT basis, source, actor_id, input_rate, output_rate, cache_read_rate, cache_write_rate
    FROM beichen_price_versions WHERE credential_id = $1::uuid AND model = $2::text AND track = $3::text
    ORDER BY effective_at DESC, id DESC LIMIT 1
  ) previous
  WHERE ROW(previous.basis, previous.source, previous.actor_id, previous.input_rate, previous.output_rate, previous.cache_read_rate, previous.cache_write_rate)
    IS NOT DISTINCT FROM ROW($4::text, $5::text, $6::uuid, $7::bigint, $8::bigint, $9::bigint, $10::bigint)
)`, credentialID, model, track, basis, source, actor, rates.Input, rates.Output, rates.CacheRead, rates.CacheWrite, effectiveAt)
	return err
}
