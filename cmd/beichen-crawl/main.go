package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("beichen-crawl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fixturePath := flags.String("fixture", "", "读取保存的 channels/available JSON")
	includeKeys := flags.Bool("keys", false, "读取官网 Key 清单，用每把 Key 的 /v1/models 对照渠道页分组")
	saveKeys := flags.Bool("save-keys", false, "读取官网 Key 清单并写入 DATABASE_URL 对应的北辰存储")
	compareModels := flags.Bool("compare-models", false, "用已核对的渠道凭据读取 /v1/models 并对照官网型号")
	matchDB := flags.Bool("match-db", false, "用 DATABASE_URL 只读匹配已保存的 Key 目录和别名")
	provider := flags.String("provider", "", "指定北辰供应商 UUID，未使用 --save-keys 时同时开启 --match-db")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "请用 --help 查看 beichen-crawl 的选项")
		return 2
	}
	if *fixturePath != "" && (*includeKeys || *saveKeys || *compareModels) {
		fmt.Fprintln(stderr, "--fixture 用于离线读取，不能与 --keys、--save-keys 或 --compare-models 一起使用")
		return 2
	}

	options := beichen.CrawlOptions{
		IncludeKeys: *includeKeys, SaveKeys: *saveKeys, CompareModels: *compareModels,
		MatchDB: *matchDB || (*provider != "" && !*saveKeys),
	}
	if *provider != "" {
		id, err := uuid.Parse(*provider)
		if err != nil || id == uuid.Nil {
			fmt.Fprintln(stderr, "--provider 需要有效的北辰供应商 UUID")
			return 2
		}
		options.ProviderID = id
	}
	token := strings.TrimSpace(os.Getenv("BEICHEN_PANEL_TOKEN"))
	email := strings.TrimSpace(os.Getenv("BEICHEN_WEB_EMAIL"))
	password := os.Getenv("BEICHEN_WEB_PASSWORD")
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if *saveKeys && dsn == "" {
		fmt.Fprintln(stderr, "--save-keys 需要 DATABASE_URL 和 PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
		return 2
	}
	if *compareModels && dsn == "" {
		fmt.Fprintln(stderr, "--compare-models 需要 DATABASE_URL 和 PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
		return 2
	}
	var savedSession *beichen.LocalLoginStore
	if *fixturePath == "" {
		if store, err := beichen.OpenLocalLoginStore(); err == nil {
			exists, err := store.Exists()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if exists {
				savedSession = store
			}
		}
	}
	hasDatabaseSession := *fixturePath == "" && savedSession == nil && dsn != ""
	localConfig := strings.TrimSpace(os.Getenv("BEICHEN_SESSION_LOCAL_FILE"))
	if localConfig == "" {
		localConfig = "deploy/beichen-session.local.env"
	}
	_, localConfigErr := os.Stat(localConfig)
	hasCredentials := savedSession != nil || hasDatabaseSession || !errors.Is(localConfigErr, os.ErrNotExist) || token != "" || (email != "" && password != "")
	if !hasCredentials {
		fmt.Fprintln(stderr, "未提供真实凭据，仅 fixture")
	}
	if *fixturePath != "" {
		var err error
		options.Fixture, err = os.ReadFile(*fixturePath)
		if err != nil {
			fmt.Fprintln(stderr, "读取 fixture 文件失败")
			return 1
		}
	} else if !hasCredentials {
		fmt.Fprintln(stderr, "请先运行 go run ./cmd/beichen-session login --window 登录，或用 --fixture 读取本地样例")
		return 2
	}

	var database *sql.DB
	if (options.MatchDB || hasDatabaseSession || options.SaveKeys || options.CompareModels) && dsn != "" {
		var err error
		database, err = sql.Open(db.DriverName, dsn)
		if err != nil {
			fmt.Fprintln(stderr, "DATABASE_URL 无法用于读取 Key 目录")
			return 1
		}
		defer database.Close()
	}
	var manager *crypto.Manager
	if hasDatabaseSession || options.SaveKeys || options.CompareModels {
		cfg, err := config.LoadProviderCatalogWorker()
		if err != nil {
			fmt.Fprintln(stderr, "使用北辰数据库会话或保存 Key 清单需要 DATABASE_URL 和有效的 PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
			return 2
		}
		manager, err = crypto.NewManager(nil, nil, nil, cfg.ProviderEncryptionKey, 0)
		if err != nil {
			fmt.Fprintln(stderr, "初始化北辰会话加密服务失败")
			return 1
		}
	}
	service := beichen.New(database, manager)
	if savedSession != nil {
		service.UseLocalLogin(savedSession)
	} else if options.Fixture == nil {
		if err := service.ConfigureImportedPanelSession(); err != nil && !hasDatabaseSession {
			fmt.Fprintln(stderr, err)
			return 2
		}
		options.PanelToken = token
		if token == "" && email != "" && password != "" {
			if err := service.ConfigureWebSession(email, password); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	report, crawlErr := service.CrawlDirectory(ctx, options)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "输出抓取报告失败")
		return 1
	}
	if crawlErr != nil {
		fmt.Fprintln(stderr, crawlErr)
		return 1
	}
	return 0
}
