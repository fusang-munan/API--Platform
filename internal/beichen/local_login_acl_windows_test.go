package beichen

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLocalLoginPrivateWindowsACL(t *testing.T) {
	store := &LocalLoginStore{dir: t.TempDir()}
	if err := store.withLock(context.Background(), func() error {
		return store.save(localPanelLogin{Access: "fixture-local-acl"})
	}); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{localSessionFile, localSessionKey} {
		descriptor, err := windows.GetNamedSecurityInfo(filepath.Join(store.dir, name), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := descriptor.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("%s inherits parent permissions", name)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil || dacl.AceCount != 1 || !strings.Contains(descriptor.String(), user.User.Sid.String()) {
			t.Fatalf("%s grants access outside the current Windows account", name)
		}
	}
}
