package labtls

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const testURL = "https://school.example:8443/"

func TestIdentityReload(t *testing.T) {
	for _, publicURL := range []string{testURL, "http://192.0.2.1", "https://[2001:db8::1]:8443"} {
		t.Run(publicURL, func(t *testing.T) {
			dir := t.TempDir()
			first, err := LoadOrCreate(dir, publicURL)
			if err != nil {
				t.Fatal(err)
			}
			host, err := publicHost(publicURL)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.Leaf.VerifyHostname(host); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("%x", sha256.Sum256(first.Certificate[0]))
			if Fingerprint(first) != want || Fingerprint(tls.Certificate{}) != "" {
				t.Fatal("incorrect leaf fingerprint")
			}
			output, err := identityProcess(dir, publicURL).Output()
			if err != nil || string(output) != want {
				t.Fatalf("restart did not load the same fingerprint: %v", err)
			}
			info, err := os.Stat(filepath.Join(dir, identityName))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatal("identity file permissions must be 0600")
			}
		})
	}
}

func TestConcurrentIdentityProcesses(t *testing.T) {
	dir := t.TempDir()
	const count = 8
	type result struct {
		fingerprint string
		err         error
	}
	results := make(chan result, count)
	for i := 0; i < count; i++ {
		go func() {
			output, err := identityProcess(dir, testURL).Output()
			results <- result{fingerprint: string(output), err: err}
		}()
	}
	cert, err := LoadOrCreate(dir, testURL)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		got := <-results
		if got.err != nil || got.fingerprint != Fingerprint(cert) {
			t.Errorf("concurrent process did not load the published identity: %v", got.err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != identityName {
		t.Fatal("expected exactly one published identity and no staging files")
	}
}

func identityProcess(dir, publicURL string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityProcessHelper$")
	cmd.Env = append(os.Environ(), "LABTLS_TEST_DIR="+dir, "LABTLS_TEST_URL="+publicURL)
	return cmd
}

func TestIdentityProcessHelper(t *testing.T) {
	dir := os.Getenv("LABTLS_TEST_DIR")
	if dir == "" {
		return
	}
	cert, err := LoadOrCreate(dir, os.Getenv("LABTLS_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// The subprocess emits only the public certificate fingerprint.
	fmt.Print(Fingerprint(cert))
	os.Exit(0)
}

func TestInvalidIdentityIsPreserved(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		host   string
		issued time.Time
	}{
		{name: "wrong SAN", host: "other.example", issued: now},
		{name: "expired", host: "school.example", issued: now.AddDate(-2, 0, 0)},
		{name: "not yet valid", host: "school.example", issued: now.Add(time.Hour)},
		{name: "damaged PEM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			data := []byte("damaged identity")
			if tc.host != "" {
				var err error
				data, err = generateIdentity(tc.host, tc.issued)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, identityName)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrCreate(dir, testURL); err == nil {
				t.Fatal("invalid identity was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("invalid identity file was changed")
			}
		})
	}
}

func TestRejectWritableTLSDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix volume modes")
	}
	dir := t.TempDir()
	first, err := LoadOrCreate(dir, testURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir, testURL); err == nil {
		t.Fatal("accepted a group-writable TLS volume")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadOrCreate(dir, testURL)
	if err != nil || Fingerprint(reloaded) != Fingerprint(first) {
		t.Fatal("existing protected identity did not survive restoring volume permissions", err)
	}
}

func TestInvalidPublicURL(t *testing.T) {
	for _, input := range []string{"school.example", "https:///path", "ftp://school.example", "https://user@school.example", "https://*.example", "https://[school.example]"} {
		dir := t.TempDir()
		if _, err := LoadOrCreate(dir, input); err == nil {
			t.Fatal("invalid PUBLIC_APP_URL was accepted")
		}
		if _, err := os.Stat(filepath.Join(dir, identityName)); !os.IsNotExist(err) {
			t.Fatal("invalid PUBLIC_APP_URL must not publish an identity")
		}
	}
}
