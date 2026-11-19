package schoolseal

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// An empty release contains no secrets and can publish the school's public key.
// The next approved main release can carry the encrypted configuration.
//
//go:embed school-config.sealed.json
var release []byte

const MaxPlaintext = 64 << 10

type Channel struct {
	Channel string `json:"channel"`
	APIKey  string `json:"api_key"`
}

type Config struct {
	Channels    []Channel `json:"channels"`
	WebEmail    string    `json:"web_email,omitempty"`
	WebPassword string    `json:"web_password,omitempty"`
}

type envelope struct {
	Recipient
	Ciphertext string `json:"ciphertext"`
}

func (cfg Config) Validate() error {
	required := map[string]bool{"Gemini": true, "GM1": true, "G3": true, "CC1": true}
	keys := make(map[string]bool)
	for _, c := range cfg.Channels {
		if !required[c.Channel] || keys[c.APIKey] || len(c.APIKey) == 0 || len(c.APIKey) > 4096 ||
			strings.IndexFunc(c.APIKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return errors.New("学校配置须包含四条不重复的北辰渠道和对应的有效 Key")
		}
		delete(required, c.Channel)
		keys[c.APIKey] = true
	}
	if len(required) != 0 {
		return errors.New("学校配置缺少北辰渠道")
	}
	if cfg.WebEmail == "" && cfg.WebPassword == "" {
		return nil
	}
	addr, err := mail.ParseAddress(cfg.WebEmail)
	if err != nil || addr.Address != cfg.WebEmail || len(cfg.WebEmail) > 254 || cfg.WebPassword == "" || len(cfg.WebPassword) > 4096 {
		return errors.New("北辰网页登录配置须包含有效邮箱和密码")
	}
	return nil
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("学校配置 JSON 无效")
	}
	return nil
}

// Validate checks the same public key that Seal will use, including during CLI preflight.
func (r Recipient) Validate() error {
	_, err := r.decodedKey()
	return err
}

func (r Recipient) decodedKey() (*[32]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(r.PublicKey)
	if r.Algorithm != Algorithm || err != nil || len(raw) != 32 {
		return nil, errors.New("学校收件公钥格式无效")
	}
	pub := (*[32]byte)(raw)
	// Sealed boxes require a non-low-order X25519 peer.
	if _, err := curve25519.X25519(curve25519.Basepoint, pub[:]); err != nil {
		return nil, errors.New("学校收件公钥无效")
	}
	return pub, nil
}

func Seal(r Recipient, config Config) ([]byte, error) {
	pub, err := r.decodedKey()
	if err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(config)
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

func (id *Identity) OpenRelease() (*Config, error) { return id.open(release) }

func HasRelease() bool { return !emptyRelease(release) }

func emptyRelease(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func (id *Identity) open(raw []byte) (*Config, error) {
	if emptyRelease(raw) {
		return nil, nil
	}
	var e envelope
	if len(raw) > 2*MaxPlaintext || decode(raw, &e) != nil || e.Recipient != id.Recipient() {
		return nil, errors.New("配置密文格式无效或收件方与本校不符")
	}
	cipher, err := base64.StdEncoding.Strict().DecodeString(e.Ciphertext)
	if err != nil || len(cipher) < box.AnonymousOverhead || len(cipher) > MaxPlaintext+box.AnonymousOverhead {
		return nil, errors.New("学校配置密文长度或编码错误")
	}
	if _, err := curve25519.X25519(id.private[:], cipher[:32]); err != nil {
		return nil, errors.New("学校配置密文无效")
	}
	plain, ok := box.OpenAnonymous(nil, cipher, &id.public, &id.private)
	if !ok {
		return nil, errors.New("学校配置密文校验失败")
	}
	defer clear(plain)
	var result Config
	if err := decode(plain, &result); err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}
