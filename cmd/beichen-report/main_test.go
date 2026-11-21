package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardCommandRejectsInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--range", "week"},
		{"--granularity", "hour"},
		{"--provider", "00000000-0000-0000-0000-000000000001"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("wrong validation result for %v", args)
		}
	}
}

func TestDashboardReconcileRejectsRemoteAndHidesDSN(t *testing.T) {
	for _, dsn := range []string{
		"postgres://test:fixture-password@db.example.invalid/nebula?sslmode=disable",
		"host=127.0.0.1,db.example.invalid dbname=nebula password=fixture-password sslmode=disable",
		"postgres://fixture-password:",
	} {
		db, err := openLocalReportDB(dsn)
		if db != nil || err == nil || strings.Contains(err.Error(), "fixture-password") {
			t.Fatal("expected safe local database validation")
		}
	}
}

func TestDashboardReportSavePreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "beichen.json")
	data := []byte("{\"currency\":\"CNY\"}\n")
	if err := saveReport(path, data); err != nil {
		t.Fatal(err)
	}
	if err := saveReport(path, []byte("{}")); err == nil {
		t.Fatal("overwrote existing report")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("did not preserve the original report")
	}
}
