// Package labtls persists the TLS identity for a maintenance listener.
// Never log, print, or transmit the private key or the combined identity file.
package labtls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const identityName = "identity.pem"

// LoadOrCreate loads the identity for publicURL or creates it in a trusted,
// private directory. An existing invalid identity is never replaced.
func LoadOrCreate(dir string, publicURL string) (tls.Certificate, error) {
	host, err := publicHost(publicURL)
	if err != nil {
		return tls.Certificate{}, err
	}
	if dir == "" {
		return tls.Certificate{}, errors.New("maintenance TLS identity directory is empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create maintenance TLS directory: %w", err)
	}
	path := filepath.Join(dir, identityName)
	cert, err := loadIdentity(path, host, time.Now())
	if !errors.Is(err, os.ErrNotExist) {
		return cert, err
	}
	data, err := generateIdentity(host, time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := publishIdentity(dir, path, data); err != nil {
		return tls.Certificate{}, err
	}
	return loadIdentity(path, host, time.Now())
}

// Fingerprint returns the lowercase SHA-256 hex digest of the leaf DER.
// It returns an empty string when there is no leaf and never accesses the key.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 || len(cert.Certificate[0]) == 0 {
		return ""
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(digest[:])
}

func publicHost(publicURL string) (string, error) {
	u, err := url.ParseRequestURI(publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", errors.New("invalid PUBLIC_APP_URL for maintenance TLS")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 || strings.HasPrefix(u.Host, "[") {
		return "", errors.New("invalid PUBLIC_APP_URL hostname for maintenance TLS")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid PUBLIC_APP_URL hostname for maintenance TLS")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("invalid PUBLIC_APP_URL hostname for maintenance TLS")
			}
		}
	}
	return host, nil
}

func generateIdentity(host string, now time.Time) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate maintenance TLS key: %w", err)
	}
	leaf := &x509.Certificate{
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}
	// Generate a positive random serial, including on Go versions that require one.
	serial := make([]byte, 16)
	if _, err := rand.Read(serial); err != nil {
		return nil, fmt.Errorf("generate maintenance TLS serial: %w", err)
	}
	serial[0] = serial[0]&0x7f | 0x40
	leaf.SerialNumber = new(big.Int).SetBytes(serial)
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create maintenance TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode maintenance TLS key: %w", err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...), nil
}

func publishIdentity(dir, path string, data []byte) error {
	file, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("stage maintenance TLS identity: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write maintenance TLS identity: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync maintenance TLS identity: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close maintenance TLS identity: %w", err)
	}
	// Link publishes a complete file and cannot overwrite a concurrent winner.
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish maintenance TLS identity: %w", err)
	}
	return nil
}

func loadIdentity(path, host string, now time.Time) (tls.Certificate, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("inspect maintenance TLS identity: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		return tls.Certificate{}, errors.New("maintenance TLS identity must be a regular file with mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("read maintenance TLS identity: %w", err)
	}
	certBlock, rest := pem.Decode(data)
	keyBlock, trailing := pem.Decode(rest)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" ||
		keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" || len(bytes.TrimSpace(trailing)) != 0 ||
		!bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(append(pem.EncodeToMemory(certBlock), pem.EncodeToMemory(keyBlock)...))) {
		return tls.Certificate{}, errors.New("invalid maintenance TLS identity PEM")
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return tls.Certificate{}, errors.New("invalid maintenance TLS certificate or private key")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, errors.New("invalid maintenance TLS leaf certificate")
	}
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth ||
		len(leaf.UnknownExtKeyUsage) != 0 || len(leaf.UnhandledCriticalExtensions) != 0 ||
		!bytes.Equal(leaf.RawIssuer, leaf.RawSubject) ||
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return tls.Certificate{}, errors.New("invalid maintenance TLS certificate profile")
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("maintenance TLS certificate is outside its validity period")
	}
	if !matchesHost(leaf, host) {
		return tls.Certificate{}, errors.New("maintenance TLS certificate SAN does not match PUBLIC_APP_URL")
	}
	cert.Leaf = leaf
	return cert, nil
}

func matchesHost(leaf *x509.Certificate, host string) bool {
	if len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 1 && leaf.IPAddresses[0].Equal(ip)
	}
	return len(leaf.IPAddresses) == 0 && len(leaf.DNSNames) == 1 && strings.EqualFold(leaf.DNSNames[0], host)
}
