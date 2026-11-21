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
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("beichen-crawl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fixturePath := flags.String("fixture", "", "读取保存的 channels/available JSON")
	matchDB := flags.Bool("match-db", false, "用 DATABASE_URL 只读匹配已保存的 Key 目录和别名")
	provider := flags.String("provider", "", "指定北辰供应商 UUID，同时开启 -match-db")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "请用 --fixture、--match-db 或 --provider 传入选项")
		return 2
	}

	options := beichen.CrawlOptions{MatchDB: *matchDB || *provider != ""}
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
	hasCredentials := token != "" || (email != "" && password != "")
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
		fmt.Fprintln(stderr, "请提供 --fixture 路径，或设置 BEICHEN_PANEL_TOKEN 或完整的 BEICHEN_WEB_EMAIL 与 BEICHEN_WEB_PASSWORD")
		return 2
	}

	var database *sql.DB
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); options.MatchDB && dsn != "" {
		// sql.Open defers connecting until the read-only matching transaction.
		var err error
		database, err = sql.Open(db.DriverName, dsn)
		if err != nil {
			fmt.Fprintln(stderr, "DATABASE_URL 无法用于读取 Key 目录")
			return 1
		}
		defer database.Close()
	}
	service := beichen.New(database, nil)
	if options.Fixture == nil {
		options.PanelToken = token
		if token == "" {
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
