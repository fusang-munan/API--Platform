package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestExportSessionEnvWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beichen-session.env")
	if err := writeSessionEnv(path, "fixture-private-token"); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("Windows 导出文件的 ACL 仍继承父目录权限")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 || !strings.Contains(sd.String(), user.User.Sid.String()) {
		t.Fatal("Windows 导出文件的访问权限没有限定为当前账号")
	}
}
