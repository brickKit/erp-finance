// Package partition 是 Module.Start 的后台循环之一：为 event_outbox/
// event_inbox 自动创建未来的周分区（决策 54、§11.5.1）。
//
// ⚠️ finance_journal_entry_lines 不在这里——它按会计期间 LIST 分区，
// 不配后台自动建分区任务（设计计划 §9 第 6 条：开新会计年度是业务
// 动作，不是日历滚动窗口）。这一点与 erp-inventory 的月分区不同。
package partition

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const (
	checkInterval  = 24 * time.Hour
	lookAheadWeeks = 4 // 提前建好当前周 + 未来 4 周，留足缓冲
)

var weeklyPartitionedTables = []string{"event_outbox", "event_inbox"}

// Start 立刻检查一次，之后每 24 小时检查一次。单次检查失败只记日志，
// 不让整个循环退出——下一轮还有机会补上（§13.3 铁律七）。
func Start(ctx context.Context, db *sql.DB, role, schema string, logger *slog.Logger) error {
	if err := ensureAllWeekly(ctx, db, role, schema); err != nil {
		logger.Error("周分区维护失败", "error", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ensureAllWeekly(ctx, db, role, schema); err != nil {
				logger.Error("周分区维护失败", "error", err)
			}
		}
	}
}

func ensureAllWeekly(ctx context.Context, db *sql.DB, role, schema string) error {
	return besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		weekStart := mondayOf(time.Now().UTC())
		for i := 0; i <= lookAheadWeeks; i++ {
			from := weekStart.AddDate(0, 0, 7*i)
			to := from.AddDate(0, 0, 7)
			for _, table := range weeklyPartitionedTables {
				if err := ensurePartition(ctx, tx, table, from, to); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func mondayOf(t time.Time) time.Time {
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -(weekday - 1))
}

func ensurePartition(ctx context.Context, tx *sql.Tx, table string, from, to time.Time) error {
	name := fmt.Sprintf("%s_%s", table, from.Format("2006_01_02"))

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return fmt.Errorf("检查分区是否存在 %s: %w", name, err)
	}
	if exists {
		return nil
	}

	stmt := fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		name, table, from.Format("2006-01-02"), to.Format("2006-01-02"),
	)
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("建分区 %s: %w", name, err)
	}
	return nil
}
