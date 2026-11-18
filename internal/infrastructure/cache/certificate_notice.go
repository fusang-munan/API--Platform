package cache

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

func (s *Store) ReserveCertificateNotice(ctx context.Context, digest []byte) (bool, error) {
	if s == nil || s.client == nil {
		return false, errors.New("certificate notice cache unavailable")
	}
	if len(digest) != sha256.Size {
		return false, errors.New("invalid certificate notice digest")
	}
	key := "notice:{certificate-v1}:recipient:" + HashKey(digest)
	return s.client.SetNX(ctx, key, "1", 24*time.Hour).Result()
}
