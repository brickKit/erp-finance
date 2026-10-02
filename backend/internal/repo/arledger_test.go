package repo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

func setSnapshot(t *testing.T, db *sql.DB, customerID, name string) {
	t.Helper()
	if err := besdk.WithTx(context.Background(), db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return UpsertCustomerSnapshotTx(tx, customerID, name, "0", 1, nil)
	}); err != nil {
		t.Fatal(err)
	}
}

// postAR 以 legalEntityID 名义过一张销售订单凭证，返回它的应收行 id。
func postAR(t *testing.T, r *Repo, legalEntityID, customerID, amount string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
		OrderID: uniqueID("ar-order"), CustomerID: customerID, Amount: amount, LegalEntityID: legalEntityID, EventVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := r.db.QueryRowContext(ctx, `SELECT id::text FROM erp_finance.ar_ledger
		WHERE customer_id = $1 ORDER BY id DESC LIMIT 1`, customerID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func setReconciled(t *testing.T, r *Repo, arID, reconciled string) {
	t.Helper()
	if _, err := r.db.Exec(`UPDATE erp_finance.ar_ledger SET reconciled_amount = $1 WHERE id = $2`, reconciled, arID); err != nil {
		t.Fatal(err)
	}
}

// 应收行带客户名（取自消费 mdm.customer.* 维护的客户摘要副本）与未核销余额
// （amount - reconciled_amount，服务端按十进制算），到期日默认是记账当天。
func TestListARLedger_带客户名未核销余额与到期日(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	named, unnamed := uniqueID("ar-named"), uniqueID("ar-unnamed")
	setSnapshot(t, db, named, "「本地测试」华南电子")
	arID := postAR(t, r, "default", named, "100.10")
	setReconciled(t, r, arID, "30.20")
	postAR(t, r, "default", unnamed, "5.00")

	res, err := r.ListARLedger(context.Background(), ListARLedgerInput{CustomerID: named, AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("期望 1 行，实际 %d", len(res.Entries))
	}
	e := res.Entries[0]
	today := time.Now().UTC().Format("2006-01-02")
	if e.CustomerName != "「本地测试」华南电子" || e.Outstanding != "69.90" || e.DueDate != today {
		t.Fatalf("期望 customer_name=「本地测试」华南电子 outstanding=69.90 due_date=%s，实际 %q %q %q",
			today, e.CustomerName, e.Outstanding, e.DueDate)
	}

	res, err = r.ListARLedger(context.Background(), ListARLedgerInput{CustomerID: unnamed, AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].CustomerName != "" || res.Entries[0].Outstanding != "5.00" {
		t.Fatalf("没有客户摘要副本时 customer_name 为空串、未核销余额等于金额，实际 %+v", res.Entries)
	}
}

// 数据范围：只授权法人 A 的调用者看不到法人 B 的应收行（两个法人都真实有数据）。
func TestListARLedger_看不到别的法人的应收(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	a, b := newLegalEntity(t, ctx, r, "ar-le-a"), newLegalEntity(t, ctx, r, "ar-le-b")
	customer := uniqueID("ar-scope-cust")
	mine := postAR(t, r, a, customer, "1.00")
	other := postAR(t, r, b, customer, "2.00")

	res, err := r.ListARLedger(ctx, ListARLedgerInput{CustomerID: customer, AllowedLegalEntityIDs: []string{a}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].ID != mine {
		t.Fatalf("只授权法人 A，应该恰好看到 A 的那一行 %s（看不到 B 的 %s），实际 %+v", mine, other, res.Entries)
	}
	res, err = r.ListARLedger(ctx, ListARLedgerInput{CustomerID: customer, AllowedLegalEntityIDs: nil})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 {
		t.Fatalf("没有任何法人授权时一行都不该看到，实际 %d 行", len(res.Entries))
	}
}
