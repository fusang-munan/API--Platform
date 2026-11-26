package schoolseal

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// A session release contains only the web identity needed to read the private
// price and usage APIs. It is kept separate from the channel release so a new
// login session can be rotated without asking the school to re-enter four keys.
//
//go:embed school-session.sealed.json
var sessionRelease []byte

type Session struct {
	WebEmail     string    `json:"web_email"`
	WebAccess    string    `json:"web_access_token"`
	WebRefresh   string    `json:"web_refresh_token"`
	WebExpiresAt time.Time `json:"web_expires_at"`
}

func validWebToken(token string) bool {
	if token == "" || len(token) > 4096 || strings.HasPrefix(strings.ToLower(token), "sk-") {
		return false
	}
	return strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r >= 127 }) < 0
}

func (session Session) Validate() error {
	email := strings.TrimSpace(session.WebEmail)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || email != session.WebEmail {
		return errors.New("北辰网页会话需要有效的账号邮箱")
	}
	if !validWebToken(session.WebAccess) || !validWebToken(session.WebRefresh) {
		return errors.New("北辰网页会话缺少有效的访问令牌或续期令牌")
	}
	if session.WebExpiresAt.IsZero() {
		return errors.New("北辰网页会话缺少访问令牌到期时间")
	}
	return nil
}

func SealSession(r Recipient, session Session) ([]byte, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return sealValue(r, session)
}

func (id *Identity) OpenSessionRelease() (*Session, error) {
	if emptyRelease(sessionRelease) {
		return nil, nil
	}
	var session Session
	if err := id.openValue(sessionRelease, &session); err != nil {
		return nil, err
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return &session, nil
}

func HasSessionRelease() bool { return !emptyRelease(sessionRelease) }

func SessionReleaseDigest() string {
	sum := sha256.Sum256(sessionRelease)
	return hex.EncodeToString(sum[:])
}

func sealValue(r Recipient, value any) ([]byte, error) {
	pub, err := r.decodedKey()
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(value)
	if err != nil || len(plain) > MaxPlaintext {
		return nil, errors.New("学校配置内容过长或编码失败")
	}
	defer clear(plain)
	cipher, err := box.SealAnonymous(nil, plain, pub, nil)
	if err != nil {
		return nil, errors.New("加密学校配置失败")
	}
	return json.Marshal(envelope{Recipient: r, Ciphertext: base64.StdEncoding.EncodeToString(cipher)})
}

func (id *Identity) openValue(raw []byte, dst any) error {
	var e envelope
	if len(raw) > 2*MaxPlaintext || decode(raw, &e) != nil || e.Recipient != id.Recipient() {
		return errors.New("配置密文格式无效或收件方与本校不符")
	}
	cipher, err := base64.StdEncoding.Strict().DecodeString(e.Ciphertext)
	if err != nil || len(cipher) < box.AnonymousOverhead || len(cipher) > MaxPlaintext+box.AnonymousOverhead {
		return errors.New("学校配置密文长度或编码错误")
	}
	if _, err := curve25519.X25519(id.private[:], cipher[:32]); err != nil {
		return errors.New("学校配置密文无效")
	}
	plain, ok := box.OpenAnonymous(nil, cipher, &id.public, &id.private)
	if !ok {
		return errors.New("学校配置密文校验失败")
	}
	defer clear(plain)
	return decode(plain, dst)
}
