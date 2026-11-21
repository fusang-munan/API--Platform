package beichen

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const localSessionFile = "beichen-session.json"
const localSessionKey = "beichen-session.key"
const localSessionLimit = 256 << 10
const localSessionAAD = "Nebula Beichen local session v1"

var errLocalSessionStorage = errors.New("无法安全读写本机北辰会话，请检查 Nebula 目录和文件权限")
var errLocalSessionKey = errors.New("无法读取本机北辰会话密钥，请检查 beichen-session.key 和当前系统账号")
var errLocalSessionCipher = errors.New("本机北辰会话解密失败，请在本机登录窗口重新登录")

type LocalLoginStore struct {
	dir string
}

func OpenLocalLoginStore() (*LocalLoginStore, error) {
	base, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		base, err = os.UserCacheDir() // %LOCALAPPDATA%, independent of the working directory.
	}
	if err != nil || !filepath.IsAbs(base) {
		return nil, errLocalSessionStorage
	}
	return &LocalLoginStore{dir: filepath.Join(base, "Nebula")}, nil
}

func (store *LocalLoginStore) Exists() (bool, error) {
	info, err := os.Lstat(filepath.Join(store.dir, localSessionFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errLocalSessionStorage
	}
	return true, nil
}

// An OS lock serializes callers across the window and CLI processes. In
// particular, two requests must never spend the same rotating refresh token.
func (store *LocalLoginStore) withLock(ctx context.Context, work func() error) error {
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		return errLocalSessionStorage
	}
	file, err := os.OpenFile(filepath.Join(store.dir, "beichen-session.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errLocalSessionStorage
	}
	defer file.Close() // Closing the handle releases the lock after exit or a crash.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryLocalSessionLock(file)
		if err != nil {
			return errLocalSessionStorage
		}
		if locked {
			return work()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type sealedLocalLogin struct {
	Version    int    `json:"version"`
	Ciphertext []byte `json:"ciphertext"`
}

func (store *LocalLoginStore) load() (localPanelLogin, error) {
	var session localPanelLogin
	raw, err := readPrivateSessionFile(filepath.Join(store.dir, localSessionFile), localSessionLimit)
	if err != nil {
		return session, err
	}
	var sealed sealedLocalLogin
	if json.Unmarshal(raw, &sealed) != nil || sealed.Version != 1 {
		return session, errLocalSessionCipher
	}
	aead, err := store.aead(false)
	if err != nil {
		return session, err
	}
	nonceLength := aead.NonceSize()
	if len(sealed.Ciphertext) < nonceLength+aead.Overhead() {
		return session, errLocalSessionCipher
	}
	plain, err := aead.Open(nil, sealed.Ciphertext[:nonceLength], sealed.Ciphertext[nonceLength:], []byte(localSessionAAD))
	if err != nil {
		return session, errLocalSessionCipher
	}
	defer clear(plain)
	if json.Unmarshal(plain, &session) != nil || !validPanelBearer(session.Access) {
		return localPanelLogin{}, errLocalSessionCipher
	}
	return session, nil
}

func (store *LocalLoginStore) save(session localPanelLogin) error {
	aead, err := store.aead(true)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(session)
	if err != nil {
		return errLocalSessionStorage
	}
	defer clear(plain)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return errLocalSessionStorage
	}
	sealed, err := json.Marshal(sealedLocalLogin{
		Version: 1, Ciphertext: aead.Seal(nonce, nonce, plain, []byte(localSessionAAD)),
	})
	if err != nil || len(sealed) > localSessionLimit {
		return errLocalSessionStorage
	}
	return writePrivateSessionFile(filepath.Join(store.dir, localSessionFile), sealed)
}

func (store *LocalLoginStore) aead(create bool) (cipher.AEAD, error) {
	path := filepath.Join(store.dir, localSessionKey)
	key, err := readPrivateSessionFile(path, 32)
	if errors.Is(err, os.ErrNotExist) && create {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err == nil {
			err = writePrivateSessionFile(path, key)
		}
	}
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, errLocalSessionKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errLocalSessionKey
	}
	return cipher.NewGCM(block)
}

func readPrivateSessionFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errLocalSessionStorage
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errLocalSessionStorage
	}
	defer file.Close()
	if err := restrictLocalSessionFile(file); err != nil {
		return nil, errLocalSessionStorage
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		clear(raw)
		return nil, errLocalSessionStorage
	}
	return raw, nil
}

func writePrivateSessionFile(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".beichen-session-*.tmp")
	if err != nil {
		return errLocalSessionStorage
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := restrictLocalSessionFile(file); err != nil {
		return errLocalSessionStorage
	}
	if _, err := file.Write(raw); err != nil {
		return errLocalSessionStorage
	}
	if err := file.Sync(); err != nil {
		return errLocalSessionStorage
	}
	if err := file.Close(); err != nil {
		return errLocalSessionStorage
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errLocalSessionStorage
	}
	return nil
}
