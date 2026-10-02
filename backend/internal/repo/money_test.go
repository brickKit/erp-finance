package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

// 金额是十进制字符串，校验与比较必须精确到分：借贷平衡、额度判定、事件里的
// 金额都不能经过 float64（大额时差一分会被抹平，"NaN" 之类的字面量会混过校验）。

func TestPostManualEntry_金额不是两位以内的十进制数时拒绝(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	for _, amt := range []string{"NaN", "Inf", "1e2", "1.005", " 1.00", "+1.00", "0x10", "1.", ".5", "12345678901234567.00"} {
		_, err := r.PostManualEntry(context.Background(), PostManualEntryInput{
			IdempotencyKey: uniqueID("bad-amount"), LegalEntityID: "default", AllowedLegalEntityIDs: defaultLegalEntities,
			Lines: []Line{{AccountCode: "1405", Debit: amt}, {AccountCode: "2202", Credit: amt}},
		})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("金额 %q 应该被拒绝（ErrInvalidArgument），实际：%v", amt, err)
		}
	}
}

func TestPostManualEntry_大额借贷差一分也拒绝(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	_, err := r.PostManualEntry(context.Background(), PostManualEntryInput{
		IdempotencyKey: uniqueID("big-unbalanced"), LegalEntityID: "default", AllowedLegalEntityIDs: defaultLegalEntities,
		Lines: []Line{
			{AccountCode: "1405", Debit: "9999999999999999.99"},
			{AccountCode: "2202", Credit: "9999999999999999.98"},
		},
	})
	if !errors.Is(err, ErrUnbalancedEntry) {
		t.Fatalf("借方比贷方多一分应该报 ErrUnbalancedEntry，实际：%v", err)
	}
}

func TestPostManualEntry_零点一加零点二等于零点三(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	e, err := r.PostManualEntry(context.Background(), PostManualEntryInput{
		IdempotencyKey: uniqueID("tenth"), LegalEntityID: "default", AllowedLegalEntityIDs: defaultLegalEntities,
		Lines: []Line{
			{AccountCode: "1405", Debit: "0.10"},
			{AccountCode: "1405", Debit: "0.20"},
			{AccountCode: "2202", Credit: "0.30"},
		},
	})
	if err != nil {
		t.Fatalf("0.10 + 0.20 = 0.30 是平衡的，实际报错：%v", err)
	}
	if got := voucherAmount(t, db, e.ID); got != "0.30" {
		t.Fatalf("finance.voucher.posted.v1 的 amount 期望 \"0.30\"，实际 %q", got)
	}
}

// 已用额度只比额度多一分，也是超限：判定不能经过 float64。
func TestPostSalesOrderEntry_大额超限一分也发creditRejected(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	customerID := uniqueID("cust-bigl")
	orderID := uniqueID("order-bigl")
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return UpsertCustomerSnapshotTx(tx, customerID, "", "9999999999999999.98", 1, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
		OrderID: orderID, CustomerID: customerID, Amount: "9999999999999999.99", EventVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM erp_finance.event_outbox
		WHERE subject = 'finance.credit.rejected.v1' AND aggregate_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("已用额度比额度多一分，期望 1 条 finance.credit.rejected.v1，实际 %d 条", n)
	}
	var entryID string
	if err := db.QueryRow(`SELECT id FROM erp_finance.finance_journal_entries
		WHERE source_component = 'erp-sales' AND source_doc_id = $1`, orderID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if got := voucherAmount(t, db, entryID); got != "9999999999999999.99" {
		t.Fatalf("finance.voucher.posted.v1 的 amount 期望 \"9999999999999999.99\"，实际 %q", got)
	}
}

// voucherAmount 取某张凭证过账时发出的 finance.voucher.posted.v1 的 amount。
func voucherAmount(t *testing.T, db *sql.DB, entryID string) string {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`SELECT payload FROM erp_finance.event_outbox
		WHERE subject = 'finance.voucher.posted.v1' AND aggregate_id = $1`, entryID).Scan(&raw); err != nil {
		t.Fatalf("查 finance.voucher.posted.v1：%v", err)
	}
	var p struct {
		Amount string `json:"amount"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Amount
}
