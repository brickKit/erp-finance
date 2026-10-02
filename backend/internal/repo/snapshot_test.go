package repo

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

// 事件里的 credit_limit 来自 mdm-customer，不受本组件控制：已发布的 mdm/customer 2.0.0
// 用 ParseFloat 校验额度，"NaN" 能通过，NUMERIC(18,2) 也照存。NaN 一旦进了摘要副本，
// 这个客户的每一张销售订单都过不了账（exceedsLimit 拒绝解析），应收凭证随事务回滚，
// 事件不重投，应收静默丢失。

func creditLimitOf(t *testing.T, db *sql.DB, customerID string) string {
	t.Helper()
	var limit string
	if err := db.QueryRow(`SELECT credit_limit::text FROM erp_finance.customer_credit_snapshots
		WHERE customer_id = $1`, customerID).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	return limit
}

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func assertWarnNamesCustomer(t *testing.T, logs *bytes.Buffer, customerID string) {
	t.Helper()
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, customerID) {
		t.Errorf("期望一条点名客户 %s 的 Warn，实际日志：%q", customerID, out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("上游给了坏额度不是要运维处理的错误，不该记 ERROR：%q", out)
	}
}

func TestUpsertCustomerSnapshot_不合法的额度按未配置存且订单照常过账(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	for _, bad := range []string{"NaN", "Infinity", "-5.00", "1e3"} {
		customerID := uniqueID("cust-badlimit")
		logger, logs := captureLogger()
		if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
			return UpsertCustomerSnapshotTx(tx, customerID, "坏额度客户", bad, 1, logger)
		}); err != nil {
			t.Errorf("额度 %q：摘要副本照常写（名字要更新），不该报错：%v", bad, err)
			continue
		}
		assertWarnNamesCustomer(t, logs, customerID)
		if got := creditLimitOf(t, db, customerID); got != "0.00" {
			t.Errorf("额度 %q 应该按未配置存成 0.00，库里是 %q", bad, got)
		}
		if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
			OrderID: uniqueID("order-badlimit"), CustomerID: customerID, Amount: "10.00", EventVersion: 1,
		}); err != nil {
			t.Errorf("额度 %q 的客户下单应该照常过账，实际：%v", bad, err)
		}
	}
}

// 1.x 的库里可能已经有 NaN 行（1.x 不校验、也不拦）：升级后过账不能因为它失败。
func TestPostSalesOrderEntry_库里已有的NaN额度按未配置处理(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	customerID := uniqueID("cust-oldnan")
	orderID := uniqueID("order-oldnan")
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO customer_credit_snapshots (customer_id, name, credit_limit, version)
			VALUES ($1, '', 'NaN', 1)`, customerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	logger, logs := captureLogger()
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return PostSalesOrderEntryTx(ctx, tx, SalesOrderEventInput{
			OrderID: orderID, CustomerID: customerID, Amount: "10.00", EventVersion: 1,
		}, logger)
	}); err != nil {
		t.Fatalf("库里额度是 NaN 时应该按未配置处理、照常过账，实际：%v", err)
	}
	assertWarnNamesCustomer(t, logs, customerID)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM erp_finance.event_outbox
		WHERE subject = 'finance.credit.rejected.v1' AND aggregate_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("未配置额度不该拒绝，期望 0 条 credit.rejected，实际 %d 条", n)
	}
}
