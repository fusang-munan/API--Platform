package controlplane

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/user"
)

func TestListIdentitiesStudentOnly(t *testing.T) {
	service, mock, closeDatabase := newIdentityTestService(t)
	defer closeDatabase()

	userID := uuid.Must(uuid.NewV7())
	mock.ExpectQuery(`SELECT .* FROM "student_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV7())))
	mock.ExpectQuery(`SELECT .* FROM "mentor_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	identities, err := service.listIdentities(context.Background(), &ent.User{
		ID: userID, Status: user.StatusActive, Role: user.RoleStudent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0] != IdentityStudent {
		t.Fatalf("expected student-only identities, got %#v", identities)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListIdentitiesRequiresActiveMentorProfile(t *testing.T) {
	service, mock, closeDatabase := newIdentityTestService(t)
	defer closeDatabase()

	userID := uuid.Must(uuid.NewV7())
	mock.ExpectQuery(`SELECT .* FROM "student_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV7())))
	mock.ExpectQuery(`SELECT .* FROM "mentor_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	identities, err := service.listIdentities(context.Background(), &ent.User{
		ID: userID, Status: user.StatusActive, Role: user.RoleStudent,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range identities {
		if item == IdentityMentor {
			t.Fatal("mentor identity must not be available without active mentor profile")
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSessionIdentityRejectsUnavailableMentor(t *testing.T) {
	service, mock, closeDatabase := newIdentityTestService(t)
	defer closeDatabase()

	userID := uuid.Must(uuid.NewV7())
	mock.ExpectQuery(`SELECT .* FROM "student_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV7())))
	mock.ExpectQuery(`SELECT .* FROM "mentor_profiles"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	err := service.validateSessionIdentity(context.Background(), &ent.User{
		ID: userID, Status: user.StatusActive, Role: user.RoleStudent,
	}, IdentityMentor)
	if err == nil {
		t.Fatal("expected mentor session to be rejected without mentor profile")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newIdentityTestService(t *testing.T) (*Service, sqlmock.Sqlmock, func()) {
	t.Helper()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	driver := entsql.OpenDB(dialect.Postgres, database)
	client := ent.NewClient(ent.Driver(driver))
	return &Service{db: client, sqlDB: database, now: func() time.Time { return time.Now() }}, mock, func() {
		_ = client.Close()
	}
}
