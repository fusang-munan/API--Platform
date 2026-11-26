package beichen

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func clearedPanelAccountRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"mode", "status", "email_ciphertext", "password_ciphertext", "token_ciphertext",
		"refresh_token_ciphertext", "expires_at", "last_verified_at", "last_error",
		"failure_count", "updated_at",
	}).AddRow(panelModePassword, panelStateNone, nil, nil, nil, nil, nil, nil, "", 0,
		time.Date(2026, 11, 26, 11, 20, 0, 0, time.UTC))
}

// 教师清除登录后保留的空行只应阻止旧的环境令牌；随版本交付的学校网页
// 会话仍要能读取官网价，否则所有渠道的定价都会停在“请先配置”。
func TestClearedPanelAccountKeepsDeliveredSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := uuid.New()
	mock.ExpectQuery("FROM beichen_panel_accounts WHERE provider_id").WithArgs(provider).
		WillReturnRows(clearedPanelAccountRows())
	service := New(db, nil)
	service.ConfigurePanelFallback("environment-old-token")
	configureReviewCapture(t, service)
	used, err := readWithWebsiteKeySession(context.Background(), service, provider, "",
		func(auth panelAuthorization) (bool, error) {
			return auth.bearer == reviewCapture, nil
		})
	if err != nil || !used {
		t.Fatalf("清除后的空行未改用交付会话: err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClearedPanelAccountBlocksWithoutDeliveredSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := uuid.New()
	mock.ExpectQuery("FROM beichen_panel_accounts WHERE provider_id").WithArgs(provider).
		WillReturnRows(clearedPanelAccountRows())
	service := New(db, nil)
	service.ConfigurePanelFallback("environment-old-token")
	called := false
	_, err = readWithWebsiteKeySession(context.Background(), service, provider, "",
		func(auth panelAuthorization) (string, error) {
			called = true
			return auth.bearer, nil
		})
	if !errors.Is(err, errPanelAccountMissing) || called {
		t.Fatalf("没有交付会话时清除状态应继续阻止读取: called=%v err=%v", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
