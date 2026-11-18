package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	inframail "github.com/starprince1234/Nebula-api/internal/infrastructure/mail"
)

type noticeTestSender func(context.Context, string, string, string) error

func (f noticeTestSender) Send(ctx context.Context, to, subject, body string) error {
	return f(ctx, to, subject, body)
}

func TestCertificateNoticeDispatch(t *testing.T) {
	const email = "student@example.invalid"
	const release = "1.2.3"
	const origin = "https://school.example.invalid:25555"
	fingerprint := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte("nebula-certificate-notice-v1:" + email))
	privateError := errors.New("simulated internal detail " + email)

	for _, tc := range []struct {
		name       string
		reserveErr error
		allowed    bool
		recipient  driver.Value
		dbErr      error
		smtpErr    error
		wantSends  int
	}{
		{name: "active recipient sends once", allowed: true, recipient: " STUDENT@EXAMPLE.INVALID ", wantSends: 1},
		{name: "unregistered or inactive", allowed: true},
		{name: "nonmatching DB result", allowed: true, recipient: "other@example.invalid"},
		{name: "duplicate"},
		{name: "cache outage", reserveErr: privateError},
		{name: "database outage", allowed: true, dbErr: privateError},
		{name: "SMTP outage", allowed: true, recipient: email, smtpErr: privateError, wantSends: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if tc.allowed {
				query := mock.ExpectQuery(`(?s)SELECT CASE WHEN count\(\*\) = 1 THEN min\(email\) END.*WHERE status = 'active'.*digest\(.*'sha256'\) = \$1.*LIMIT 2`).WithArgs(digest[:])
				if tc.dbErr != nil {
					query.WillReturnError(tc.dbErr)
				} else {
					query.WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow(tc.recipient))
				}
			}
			sends, reservations := 0, 0
			service := &Service{sqlDB: database, mailer: noticeTestSender(func(_ context.Context, to, subject, body string) error {
				sends++
				if to != email || subject != "Nebula school HTTPS certificate" ||
					body != "Release: "+release+"\nSchool HTTPS: "+origin+"\nLeaf SHA-256: "+fingerprint+"\n" {
					t.Fatal("unexpected certificate email")
				}
				return tc.smtpErr
			})}
			reserve := func(context.Context, []byte) (bool, error) {
				reservations++
				return tc.allowed && reservations == 1, tc.reserveErr
			}
			err = service.sendCertificateNotice(context.Background(), digest[:], release, origin, fingerprint, reserve)
			if tc.reserveErr != nil || tc.dbErr != nil || tc.smtpErr != nil {
				if err == nil || err.Error() != "certificate notice unavailable" || errors.Is(err, privateError) {
					t.Fatalf("expected sanitized error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.wantSends != 0 {
				if err := service.sendCertificateNotice(context.Background(), digest[:], release, origin, fingerprint, reserve); err != nil {
					t.Fatal(err)
				}
			}
			if sends != tc.wantSends || reservations == 0 {
				t.Fatalf("sends = %d, reservations = %d", sends, reservations)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCertificateNoticeRejectsUnsafeInputs(t *testing.T) {
	digest := make([]byte, sha256.Size)
	fingerprint := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		digest                       []byte
		release, origin, fingerprint string
	}{
		{digest[:31], "1.2.3", "https://school.example.invalid", fingerprint},
		{digest, "1.2.3\nextra", "https://school.example.invalid", fingerprint},
		{digest, "1.2.3", "https://school.example.invalid/redirect?next=elsewhere", fingerprint},
		{digest, "1.2.3", "http://school.example.invalid", fingerprint},
		{digest, "1.2.3", "https://school.example.invalid", fingerprint[:62]},
	} {
		err := (&Service{}).SendCertificateNotice(context.Background(), tc.digest, tc.release, tc.origin, tc.fingerprint)
		if err == nil || err.Error() != "invalid certificate notice" {
			t.Fatalf("expected validation error, got %v", err)
		}
	}
}

func TestCertificateNoticeDisablesLogSender(t *testing.T) {
	database, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := &Service{sqlDB: database, mailer: inframail.NewLogSender()}
	err = service.sendCertificateNotice(context.Background(), make([]byte, sha256.Size), "1.2.3",
		"https://school.example.invalid", strings.Repeat("ab", 32),
		func(context.Context, []byte) (bool, error) {
			t.Fatal("log-only mode should stop before reservation")
			return false, nil
		},
	)
	if err == nil {
		t.Fatal("expected unavailable")
	}
}
