package schoolseal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/privatefile"
)

type SessionStore struct {
	identity *Identity
	capture  Session
	digest   [sha256.Size]byte
	path     string
}

type savedSession struct {
	Capture [sha256.Size]byte `json:"capture"`
	Session
}

// SessionStore ties renewals to the original delivered refresh credential. A
// newly delivered capture uses its own file, even for the same account.
func (id *Identity) SessionStore(capture Session) (*SessionStore, error) {
	if err := capture.Validate(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(capture.WebRefresh))
	path := filepath.Join(id.dir, "web-session-"+hex.EncodeToString(digest[:])+".sealed.json")
	return &SessionStore{identity: id, capture: capture, digest: digest, path: path}, nil
}

func (s *SessionStore) Load() (Session, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.capture, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2*MaxPlaintext ||
		(runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		return Session{}, errors.New("学校北辰续期会话文件无效或权限错误")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return Session{}, errors.New("读取学校北辰续期会话失败")
	}
	var saved savedSession
	if s.identity.openValue(raw, &saved) != nil || saved.Capture != s.digest ||
		saved.WebEmail != s.capture.WebEmail || saved.Session.Validate() != nil {
		return Session{}, errors.New("学校北辰续期会话密文校验失败")
	}
	return saved.Session, nil
}

func (s *SessionStore) Save(access, refresh string, expiresAt time.Time) error {
	state := savedSession{Capture: s.digest, Session: Session{
		WebEmail: s.capture.WebEmail, WebAccess: access, WebRefresh: refresh, WebExpiresAt: expiresAt.UTC(),
	}}
	if err := state.Session.Validate(); err != nil {
		return err
	}
	raw, err := sealValue(s.identity.Recipient(), state)
	if err != nil {
		return err
	}
	if err := privatefile.Replace(s.path, raw); err != nil {
		return errors.New("持久保存学校北辰续期会话失败")
	}
	return nil
}
