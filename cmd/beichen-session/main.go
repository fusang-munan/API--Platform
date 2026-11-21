package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

const cliUsage = "用法：beichen-session login --window，或 status|check [--provider 供应商UUID]。学校管理模式支持 login|export --actor 教师UUID 和 import <配置文件>"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, input *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, cliUsage)
		return 0
	}
	command := args[0]
	if command == "import" {
		return importConfig(args[1:], stdout, stderr)
	}
	switch command {
	case "login", "status", "export", "check":
	default:
		fmt.Fprintln(stderr, cliUsage)
		return 2
	}
	flags := flag.NewFlagSet("beichen-session", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	providerFlag := flags.String("provider", "", "北辰供应商 UUID")
	var actorFlag, mode, origin string
	var window bool
	if command == "login" || command == "export" {
		flags.StringVar(&actorFlag, "actor", "", "当前启用的教师账号 UUID")
	}
	if command == "login" {
		flags.StringVar(&mode, "mode", "password", "password 或 token")
		flags.BoolVar(&window, "window", false, "打开本机北辰登录窗口")
	}
	if command == "export" {
		flags.StringVar(&origin, "server", os.Getenv("PUBLIC_APP_URL"), "学校服务器 HTTPS 地址")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, cliUsage)
		return 0
	} else if err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, cliUsage)
		return 2
	}
	if window {
		if flags.NFlag() != 1 {
			fmt.Fprintln(stderr, "本机登录只需指定 --window，请在窗口内选择登录方式")
			return 2
		}
		store, err := beichen.OpenLocalLoginStore()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		service := beichen.New(nil, nil)
		service.UseLocalLogin(store)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runLoginWindow(ctx, service, func(address string) error {
			if err := openDefaultBrowser(address); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "已打开北辰登录窗口。关闭标签页后，本机登录服务会退出。")
			return nil
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	var provider, actor uuid.UUID
	var err error
	if *providerFlag != "" {
		provider, err = uuid.Parse(*providerFlag)
		if err != nil || provider == uuid.Nil {
			fmt.Fprintln(stderr, "请为 --provider 指定有效的北辰供应商 UUID")
			return 2
		}
	}
	if command == "login" || command == "export" {
		actor, err = uuid.Parse(actorFlag)
		if err != nil || actor == uuid.Nil {
			fmt.Fprintln(stderr, "请为 --actor 指定有效的教师 UUID")
			return 2
		}
		if command == "login" && mode != "password" && mode != "token" {
			fmt.Fprintln(stderr, "--mode 须为 password 或 token")
			return 2
		}
	}

	if (command == "check" || command == "status") && provider == uuid.Nil {
		if store, err := beichen.OpenLocalLoginStore(); err == nil {
			exists, err := store.Exists()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if exists {
				return runLocalSessionCommand(store, command, stdout, stderr)
			}
		}
	}
	if (command == "check" || command == "status") && strings.TrimSpace(os.Getenv("DATABASE_URL")) == "" {
		service := beichen.New(nil, nil)
		if err := service.ConfigureImportedPanelSession(); err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 2
		}
		service.ConfigurePanelFallback(os.Getenv("BEICHEN_PANEL_TOKEN"))
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if command == "status" {
			err = service.CheckSharedPanelConnection(ctx, provider)
			if err == nil {
				fmt.Fprintln(stdout, "学校服务器的北辰会话可用")
			}
		} else {
			err = runCommand(ctx, service, provider, actor, command, origin, beichen.PanelAccountLogin{}, stdout, stderr)
		}
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 1
		}
		return 0
	}
	cfg, err := config.LoadProviderCatalogWorker()
	if err != nil {
		fmt.Fprintln(stderr, "请设置 DATABASE_URL 和有效的 PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
		return 2
	}
	manager, err := crypto.NewManager(nil, nil, nil, cfg.ProviderEncryptionKey, 0)
	if err != nil {
		fmt.Fprintln(stderr, "初始化北辰会话加密服务失败")
		return 1
	}
	database, err := sql.Open(db.DriverName, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "无法打开北辰会话数据库，请检查 DATABASE_URL")
		return 1
	}
	defer database.Close()
	service := beichen.New(database, manager)
	if command == "check" {
		// A bad imported profile is reported only if this provider has no
		// saved database session.
		_ = service.ConfigureImportedPanelSession()
	}
	service.ConfigurePanelFallback(os.Getenv("BEICHEN_PANEL_TOKEN"))

	var credentials beichen.PanelAccountLogin
	if command == "login" {
		credentials, err = readLogin(input, stderr, mode)
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	err = runCommand(ctx, service, provider, actor, command, origin, credentials, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}

func runLocalSessionCommand(store *beichen.LocalLoginStore, command string, stdout, stderr io.Writer) int {
	service := beichen.New(nil, nil)
	service.UseLocalLogin(store)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var err error
	if command == "status" {
		var status *beichen.LocalLoginStatus
		status, err = service.VerifyLocalLogin(ctx)
		if err == nil {
			err = writeStatus(stdout, status)
		}
	} else {
		err = runCommand(ctx, service, uuid.Nil, uuid.Nil, command, "", beichen.PanelAccountLogin{}, stdout, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func readLogin(input *os.File, prompt io.Writer, mode string) (beichen.PanelAccountLogin, error) {
	login := beichen.PanelAccountLogin{Mode: mode}
	// Require a terminal and test it before reading any input. A redirected
	// password or token could otherwise come from an insecure command line.
	restore, err := disableTerminalEcho(input)
	if err != nil {
		return login, errors.New("请在可关闭输入回显的终端里交互式登录")
	}
	if err := restore(); err != nil {
		return login, errors.New("无法恢复终端输入，请重新打开终端")
	}
	reader := bufio.NewReaderSize(input, 8192)
	if mode == "password" {
		fmt.Fprint(prompt, "北辰账号邮箱：")
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return login, errors.New("读取北辰账号邮箱失败")
		}
		login.Email = strings.TrimSpace(string(line))
	}
	restore, err = disableTerminalEcho(input)
	if err != nil {
		return login, errors.New("无法关闭终端回显，已取消输入")
	}
	if mode == "password" {
		fmt.Fprint(prompt, "北辰密码（输入隐藏）：")
	} else {
		fmt.Fprint(prompt, "北辰网页 Token（输入隐藏）：")
	}
	line, err := reader.ReadSlice('\n')
	restoreErr := restore()
	fmt.Fprintln(prompt)
	if err != nil || restoreErr != nil {
		clear(line)
		return login, errors.New("读取凭证或恢复终端输入失败")
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	clear(line)
	if mode == "password" {
		login.Password = secret
	} else {
		login.Token = secret
	}
	return login, nil
}

func runCommand(ctx context.Context, service *beichen.Service, provider, actor uuid.UUID, command string,
	origin string, credentials beichen.PanelAccountLogin, stdout, stderr io.Writer) error {
	switch command {
	case "login":
		status, err := service.LoginPanelAccount(ctx, provider, actor, credentials)
		if err != nil {
			return err
		}
		if status.Status != "active" && status.LastError != nil {
			return errors.New(*status.LastError)
		}
		return writeStatus(stdout, status)
	case "status":
		status, err := service.PanelAccountStatus(ctx, provider)
		if err != nil {
			return err
		}
		return writeStatus(stdout, status)
	case "export":
		config, err := service.ExportPanelConfiguration(ctx, provider, actor, origin)
		if err != nil {
			return err
		}
		path := filepath.Join("deploy", "beichen-session.env")
		if err := writeSessionEnv(path, config.Env()); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "已导出到 deploy/beichen-session.env")
		fmt.Fprintln(stderr, "本次导出会撤销此前导出的本机授权。")
		if note := exportPermissionsNote(); note != "" {
			fmt.Fprintln(stderr, note)
		}
	case "check":
		summary, err := service.CheckPanelAccount(ctx, provider)
		if summary.Platforms != nil {
			fmt.Fprintf(stdout, "平台 %d 个，模型 %d 个\n", *summary.Platforms, *summary.Models)
		}
		return err
	}
	return nil
}

func importConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stderr, "用法：beichen-session import <导出的 .env 文件>")
		if len(args) == 1 {
			return 0
		}
		return 2
	}
	config, err := beichen.ReadLocalPanelConfig(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "无法读取北辰本机配置，请检查文件路径或在学校服务器重新导出")
		return 1
	}
	path := filepath.Join("deploy", "beichen-session.local.env")
	if err := writeSessionEnv(path, config.Env()); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	fmt.Fprintln(stdout, "已导入到 deploy/beichen-session.local.env")
	return 0
}

func writeStatus(out io.Writer, status any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(status); err != nil {
		return errors.New("输出北辰会话状态失败")
	}
	return nil
}

func writeSessionEnv(path, contents string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".beichen-session-*.tmp")
	if err != nil {
		return errors.New("无法创建北辰会话导出文件，请从仓库根目录运行")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := restrictExportPermissions(file); err != nil {
		return errors.New("无法限制导出文件权限，已取消写入")
	}
	if _, err := file.WriteString(contents); err != nil {
		return errors.New("写入北辰会话导出文件失败")
	}
	if err := file.Sync(); err != nil {
		return errors.New("保存北辰会话导出文件失败")
	}
	if err := file.Close(); err != nil {
		return errors.New("保存北辰会话导出文件失败")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("替换北辰会话导出文件失败，请检查文件权限和占用情况")
	}
	return nil
}
