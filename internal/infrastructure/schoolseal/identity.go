// Package schoolseal delivers a Beichen configuration through the school's
// trusted release branch. Only the school keeps the decryption key.
package schoolseal

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/privatefile"
)

const (
	Algorithm  = "nacl-sealed-box-v1"
	PublicPath = "/.well-known/nebula-beichen-recipient"
)

type Recipient struct {
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

type Identity struct {
	public  [32]byte
	private [32]byte
}

func (id *Identity) Recipient() Recipient {
	return Recipient{Algorithm: Algorithm, PublicKey: base64.StdEncoding.EncodeToString(id.public[:])}
}

// LoadOrCreate must be given a service-owned directory on a persistent volume.
// Concurrent startups publish at most one key. A damaged key is never replaced.
func LoadOrCreate(dir string) (*Identity, error) {
	if dir == "" {
		return nil, errors.New("学校加密配置目录为空")
	}
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, errors.New("无法创建学校加密配置的父目录")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || (runtime.GOOS != "windows" && parentInfo.Mode().Perm()&0022 != 0 && parentInfo.Mode()&os.ModeSticky == 0) {
		return nil, errors.New("学校加密配置父目录权限不安全")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("无法创建学校加密配置目录")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("学校加密配置目录须为服务专用的私有目录")
	}
	if err := privatefile.SyncDirectory(parent); err != nil {
		return nil, errors.New("无法持久保存学校加密配置目录")
	}
	path := filepath.Join(dir, "recipient.key")
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		key, genErr := ecdh.X25519().GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, errors.New("生成学校配置密钥失败")
		}
		if err := privatefile.PublishOnce(dir, path, key.Bytes()); err != nil {
			return nil, errors.New("保存学校配置密钥失败")
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != 32 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		return nil, errors.New("学校配置密钥文件无效或权限错误")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("无法读取学校配置密钥")
	}
	defer clear(raw)
	if bytes.Equal(raw, make([]byte, 32)) {
		return nil, errors.New("学校配置密钥内容无效")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, errors.New("学校配置密钥无效")
	}
	id := &Identity{}
	copy(id.private[:], raw)
	copy(id.public[:], key.PublicKey().Bytes())
	return id, nil
}
