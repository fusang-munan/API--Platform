package certificatenotice

import (
	"context"
	"database/sql"
)

const certificateNoticeRecipientQuery = `
SELECT CASE WHEN count(*) = 1 THEN min(email) END
FROM (
    SELECT email::text AS email
    FROM users
    WHERE status = 'active'
      AND digest('nebula-certificate-notice-v1:' || lower(btrim(email::text)), 'sha256') = $1
    LIMIT 2
) AS matches`

func Recipient(ctx context.Context, database *sql.DB, digest []byte) (string, error) {
	var email sql.NullString
	err := database.QueryRowContext(ctx, certificateNoticeRecipientQuery, digest).Scan(&email)
	return email.String, err
}
