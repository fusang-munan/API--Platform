package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"

	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/certificatenotice"
	inframail "github.com/starprince1234/Nebula-api/internal/infrastructure/mail"
)

const certificateNoticeSubject = "Nebula school HTTPS certificate"

var certificateNoticeRelease = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$`)

// SendCertificateNotice requires the caller to verify the single approved
// recipient and release and enforce the once-per-process trigger.
// The public handler must use the same response for every dispatch outcome.
func (s *Service) SendCertificateNotice(ctx context.Context, emailDigest []byte, release, origin, fingerprint string) error {
	return s.sendCertificateNotice(ctx, emailDigest, release, origin, fingerprint, s.cache.ReserveCertificateNotice)
}

func (s *Service) sendCertificateNotice(ctx context.Context, emailDigest []byte, release, origin, fingerprint string,
	reserve func(context.Context, []byte) (bool, error),
) error {
	parsed, urlErr := url.Parse(origin)
	leaf, fingerprintErr := hex.DecodeString(fingerprint)
	if len(emailDigest) != sha256.Size || !certificateNoticeRelease.MatchString(release) ||
		urlErr != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.ForceQuery ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != origin ||
		fingerprintErr != nil || len(leaf) != sha256.Size {
		return domain.NewError(domain.CodeValidation, "invalid certificate notice")
	}

	unavailable := domain.NewError(domain.CodeDependencyUnavailable, "certificate notice unavailable")
	_, logOnly := s.mailer.(*inframail.LogSender)
	if s.mailer == nil || logOnly || s.sqlDB == nil {
		return unavailable
	}
	allowed, err := reserve(ctx, emailDigest)
	if err != nil {
		return unavailable
	}
	if !allowed {
		return nil
	}
	email, err := certificatenotice.Recipient(ctx, s.sqlDB, emailDigest)
	if err != nil {
		return unavailable
	}
	if email == "" {
		return nil
	}
	email, err = NormalizeEmail(email)
	if err != nil {
		return unavailable
	}
	digest := sha256.Sum256([]byte("nebula-certificate-notice-v1:" + email))
	if !bytes.Equal(digest[:], emailDigest) {
		return nil
	}
	body := "Release: " + release + "\nSchool HTTPS: " + origin + "\nLeaf SHA-256: " + fingerprint + "\n"
	if err := s.mailer.Send(ctx, email, certificateNoticeSubject, body); err != nil {
		return unavailable
	}
	return nil
}
