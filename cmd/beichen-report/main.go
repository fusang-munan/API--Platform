package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/beichen"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("beichen-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	period := flags.String("range", "7d", "统计范围 today（今日）、7d（近 7 天）、all（总计）")
	granularity := flags.String("granularity", "day", "趋势粒度 day（按天）")
	reconcile := flags.Bool("reconcile", false, "通过 DATABASE_URL 只读对照本机 PostgreSQL 账本")
	provider := flags.String("provider", "", "本地对账使用的北辰供应商 UUID")
	out := flags.String("out", "", "JSON 文件路径，默认写到 tmp/beichen-report-*.json")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*period != "today" && *period != "7d" && *period != "all") || *granularity != "day" {
		fmt.Fprintln(stderr, "请用 --help 查看范围和粒度选项")
		return 2
	}
	options := beichen.DashboardOptions{Range: *period, Granularity: *granularity, Reconcile: *reconcile}
	if *provider != "" {
		id, err := uuid.Parse(*provider)
		if !*reconcile || err != nil || id == uuid.Nil {
			fmt.Fprintln(stderr, "--provider 需要搭配 --reconcile 使用，并填写有效的供应商 UUID")
			return 2
		}
		options.ProviderID = id
	}

	var db *sql.DB
	if *reconcile {
		var err error
		db, err = openLocalReportDB(os.Getenv("DATABASE_URL"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer db.Close()
	}

	store, err := beichen.OpenLocalLoginStore()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	service := beichen.New(db, nil)
	service.UseLocalLogin(store)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	report, err := service.ReadDashboard(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "无法编码北辰报表")
		return 1
	}
	data = append(data, '\n')
	path := *out
	if path == "" {
		name := fmt.Sprintf("beichen-report-%s-%s.json", report.StartedAt.Format("20060102-150405.000000000"), options.Range)
		path = filepath.Join("tmp", name)
	}
	if err := saveReport(path, data); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stderr, "已保存报表 %s\n", path)
	if _, err := stdout.Write(data); err != nil {
		return 1
	}
	return 0
}

func openLocalReportDB(dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("本地对账需要在 DATABASE_URL 中设置本机 PostgreSQL 连接")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DATABASE_URL 的 PostgreSQL 连接格式无效")
	}
	if !loopbackHost(config.Host) {
		return nil, errors.New("本地对账只连接本机回环地址上的 PostgreSQL")
	}
	for _, alternate := range config.Fallbacks {
		if !loopbackHost(alternate.Host) {
			return nil, errors.New("DATABASE_URL 的备用地址也须指向本机回环地址")
		}
	}
	return stdlib.OpenDB(*config), nil
}

func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func saveReport(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("无法创建报表目录")
	}
	file, err := os.CreateTemp(dir, ".beichen-report-*.tmp")
	if err != nil {
		return errors.New("无法在报表目录创建临时文件")
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return errors.New("写入报表文件失败")
	}
	if err := file.Close(); err != nil {
		return errors.New("完成报表文件写入失败")
	}
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("报表目标文件已存在，请指定新的 --out 路径")
		}
		return errors.New("无法保存报表，请选择支持硬链接的本机文件系统")
	}
	return nil
}
