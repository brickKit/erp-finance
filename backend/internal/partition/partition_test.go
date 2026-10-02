package partition

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMondayOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-09-06", "2026-08-31"}, // Sunday -> 上一周一
		{"2026-08-31", "2026-08-31"}, // Monday -> 自己
		{"2026-09-02", "2026-08-31"}, // Wednesday -> 本周一
	}
	for _, c := range cases {
		in, err := time.Parse("2006-01-02", c.in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := time.Parse("2006-01-02", c.want)
		if err != nil {
			t.Fatal(err)
		}
		got := mondayOf(in)
		if !got.Equal(want) {
			t.Errorf("mondayOf(%s) = %s，期望 %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
}

// 关停时 ctx 被取消，这一轮维护跟着失败：这不是要运维处理的错误，不记 ERROR（R51）。
func TestStart_关停时ctx取消不记ERROR(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://127.0.0.1:1/none") // 不会真的连：ctx 已取消
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Start(ctx, db, "erp_finance_rw", "erp_finance", logger); err != nil {
		t.Fatalf("ctx 取消时 Start 应该返回 nil，实际：%v", err)
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Errorf("关停时 ctx 取消不该记 ERROR，实际日志：%q", out)
	}
}
