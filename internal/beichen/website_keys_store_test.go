package beichen

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestWebsiteKeysReportsStaleInventory(t *testing.T) {
	now := time.Date(2026, time.November, 22, 3, 0, 0, 0, time.UTC)
	fresh, old := now.Add(-websiteDirectoryInterval), now.Add(-2*websiteDirectoryInterval-time.Second)
	for _, tc := range []struct {
		name, stored, want string
		lastSeen           *time.Time
	}{
		{"recent read", "current", "current", &fresh},
		{"expired read", "current", "stale", &old},
		{"missing confirmation time", "current", "stale", nil},
		{"expired session takes precedence", "session_expired", "session_expired", &old},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			provider, credential := uuid.New(), uuid.New()
			service := New(db, nil)
			service.now = func() time.Time { return now }
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT p.id FROM providers p").WithArgs(provider).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(provider))
			mock.ExpectQuery("SELECT status,last_seen FROM beichen_website_key_sync").WithArgs(provider).
				WillReturnRows(sqlmock.NewRows([]string{"status", "last_seen"}).AddRow(tc.stored, tc.lastSeen))
			keyRows := sqlmock.NewRows([]string{
				"id", "name", "prefix", "group_id", "group_name", "rate", "peak_enabled", "peak_rate", "peak_start",
				"peak_end", "observed_at", "changed_at", "status", "enabled", "created_at", "credential_id", "match_status",
			}).AddRow(1, "fixture Key", nil, 2, "旧组", "0.15", nil, nil, nil, nil, nil, nil, "active", true, nil, credential, "matched")
			mock.ExpectQuery("FROM beichen_website_keys k").WithArgs(provider).WillReturnRows(keyRows)
			mock.ExpectRollback()

			view, err := service.WebsiteKeys(context.Background(), provider)
			if err != nil || view.Status != tc.want || len(view.Keys) != 1 {
				t.Fatalf("unexpected inventory status=%s, keys=%d: %v", view.Status, len(view.Keys), err)
			}
			key := view.Keys[0]
			if key.GroupID == nil || *key.GroupID != 2 || key.Rate == nil || *key.Rate != "0.15" ||
				key.CredentialID == nil || *key.CredentialID != credential || key.MatchStatus != "matched" {
				t.Fatalf("last observed Key data changed: %+v", key)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
