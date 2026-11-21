package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/beichen"
)

func TestExportSessionEnvPermissionsAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beichen-session.env")
	for _, digit := range []string{"1", "2"} {
		contents := "BEICHEN_SESSION_SERVER=https://school.example\nBEICHEN_SESSION_PROVIDER_ID=00000000-0000-4000-8000-000000000001\n" +
			"BEICHEN_SESSION_GRANT=nblbc_" + strings.Repeat(digit, 64) + "\n"
		if err := writeSessionEnv(path, contents); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal("导出未生成普通文件")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("不安全的导出权限 %o", info.Mode().Perm())
		}
		config, err := beichen.ReadLocalPanelConfig(path)
		if err != nil || config.Env() != contents {
			t.Fatal("导出文件内容不正确")
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("导出失败后留下了临时文件")
	}
}

func TestImportSessionRejectsUntrustedOriginAndExtraVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.env")
	for _, origin := range []string{"http://school.example", "https://school.example\nEXTRA=1", "https://school.example/private"} {
		contents := "BEICHEN_SESSION_SERVER=" + origin +
			"\nBEICHEN_SESSION_PROVIDER_ID=00000000-0000-4000-8000-000000000001\n" +
			"BEICHEN_SESSION_GRANT=nblbc_" + strings.Repeat("3", 64) + "\n"
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := beichen.ReadLocalPanelConfig(path); err == nil {
			t.Fatal("导入接受了无效服务器地址或额外变量")
		}
	}
}

func TestSessionLoginRejectsRedirectedSecretInput(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "redirected-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := readLogin(input, os.Stderr, "password"); err == nil {
		t.Fatal("登录没有要求支持关闭回显的交互终端")
	}
}
