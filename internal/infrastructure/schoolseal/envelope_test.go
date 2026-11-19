package schoolseal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func sampleConfig() Config {
	return Config{Channels: []Channel{{"Gemini", "fixture-gemini"}, {"CC1", "fixture-cc1"}, {"GM1", "fixture-gm1"}, {"G3", "fixture-g3"}}, WebEmail: "school@example.test", WebPassword: "sample-password"}
}

func TestEmptyAndNullRelease(t *testing.T) {
	id, err := LoadOrCreate(filepath.Join(t.TempDir(), "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	original := release
	t.Cleanup(func() { release = original })
	for _, data := range []string{"", " \r\n", "null", "  null\n"} {
		release = []byte(data)
		cfg, err := id.OpenRelease()
		if err != nil || cfg != nil || HasRelease() {
			t.Fatal("unconfigured release did not preserve enrollment")
		}
	}
	for _, data := range []string{`"null"`, "{}", "null false"} {
		release = []byte(data)
		if _, err := id.OpenRelease(); err == nil || !HasRelease() {
			t.Fatal("malformed delivery treated as unconfigured")
		}
	}
}

func TestSchoolRecipientVolumePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix volume modes")
	}
	volume := filepath.Join(t.TempDir(), "volume")
	if err := os.Mkdir(volume, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(volume, "beichen-delivery")
	id, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "recipient.key")
	for path, want := range map[string]os.FileMode{dir: 0700, key: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("unexpected service volume mode", want, err)
		}
	}
	for _, tc := range []struct {
		path          string
		mode, restore os.FileMode
	}{{dir, 0755, 0700}, {key, 0644, 0600}, {volume, 0777, 0700}} {
		if err := os.Chmod(tc.path, tc.mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(dir); err == nil {
			t.Fatal("insecure volume accepted")
		}
		if err := os.Chmod(tc.path, tc.restore); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := LoadOrCreate(dir)
	if err != nil || reloaded.Recipient() != id.Recipient() {
		t.Fatal("restoring permissions changed recipient", err)
	}
}

func TestReleaseBindsOnePersistentRecipient(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipient")
	ids := make([]*Identity, 6)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := LoadOrCreate(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for _, id := range ids {
		if id.Recipient() != ids[0].Recipient() {
			t.Fatal("concurrent startup rotated the key")
		}
	}
	cfg := sampleConfig()
	raw, err := Seal(ids[0].Recipient(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{cfg.WebEmail, cfg.WebPassword, cfg.Channels[0].APIKey} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("cleartext in release")
		}
	}
	got, err := ids[1].open(raw)
	if err != nil || got.WebPassword != cfg.WebPassword || len(got.Channels) != 4 {
		t.Fatal("restart did not decrypt the release")
	}
	if empty, err := ids[0].open(nil); err != nil || empty != nil {
		t.Fatal("first deployment should only publish the recipient")
	}
	other, err := LoadOrCreate(filepath.Join(t.TempDir(), "recipient"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.open(raw); err == nil {
		t.Fatal("wrong school accepted release")
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	cipher, _ := base64.StdEncoding.DecodeString(e.Ciphertext)
	cipher[len(cipher)-1] ^= 1
	e.Ciphertext = base64.StdEncoding.EncodeToString(cipher)
	bad, _ := json.Marshal(e)
	if _, err := ids[0].open(bad); err == nil {
		t.Fatal("tampered release accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "recipient.key"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("broken recipient overwritten")
	}
	keyPath := filepath.Join(dir, "recipient.key")
	if err := os.WriteFile(keyPath, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("zeroed private key accepted")
	}
}

func TestRejectIncompleteOrAmbiguousSuppliedConfig(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Channels = c.Channels[:3] },
		func(c *Config) { c.Channels[1] = c.Channels[0] },
		func(c *Config) { c.Channels[1].APIKey = c.Channels[0].APIKey },
		func(c *Config) { c.WebPassword = "" },
	} {
		cfg := sampleConfig()
		change(&cfg)
		if cfg.Validate() == nil {
			t.Fatal("invalid cleartext accepted")
		}
	}
	if _, err := Seal(Recipient{Algorithm: Algorithm, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}, sampleConfig()); err == nil {
		t.Fatal("invalid recipient accepted")
	}
}
