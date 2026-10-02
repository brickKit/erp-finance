package repo

import (
	"context"
	"testing"
	"time"
)

func setDueDate(t *testing.T, r *Repo, arID string, due time.Time) {
	t.Helper()
	if _, err := r.db.Exec(`UPDATE erp_finance.ar_ledger SET due_date = $1 WHERE id = $2`, due.Format("2006-01-02"), arID); err != nil {
		t.Fatal(err)
	}
}

func summarize(t *testing.T, r *Repo, in ARSummaryInput) *ARSummary {
	t.Helper()
	s, err := r.SummarizeARLedger(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 金额全是十进制字符串、精确到分：0.10 + 0.20 = 0.30。
func TestSummarizeARLedger_金额精确到分(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	le := newLegalEntity(t, context.Background(), r, "sum-le")
	customer := uniqueID("sum-cust")
	postAR(t, r, le, customer, "0.10")
	postAR(t, r, le, customer, "0.20")

	s := summarize(t, r, ARSummaryInput{AsOf: time.Now().UTC(), AllowedLegalEntityIDs: []string{le}})
	if s.TotalReceivable != "0.30" || s.TotalReconciled != "0.00" || s.Outstanding != "0.30" || s.Aging.D0To30 != "0.30" {
		t.Fatalf("期望 0.30 / 0.00 / 0.30，d0_30=0.30，实际 %+v", s)
	}
}

// 账龄 = 统计当天 − 到期日（天），按未核销余额分桶：≤30 天、31–60、61–90、>90。
// 边界日第 30 / 31、60 / 61、90 / 91 天各落在哪一桶。
func TestSummarizeARLedger_账龄按到期日分桶与边界日(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	le := newLegalEntity(t, context.Background(), r, "aging-le")
	customer := uniqueID("aging-cust")
	asOf := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	// 天数 → 金额（2 的幂，桶里的合计能唯一还原出是哪几行）
	cases := []struct {
		days   int
		amount string
	}{{0, "1.00"}, {30, "2.00"}, {31, "4.00"}, {60, "8.00"}, {61, "16.00"}, {90, "32.00"}, {91, "64.00"}}
	var lastID string
	for _, c := range cases {
		id := postAR(t, r, le, customer, c.amount)
		setDueDate(t, r, id, asOf.AddDate(0, 0, -c.days))
		lastID = id
	}
	setReconciled(t, r, lastID, "0.50") // 91 天那行核销了 0.50，只按未核销余额进桶

	s := summarize(t, r, ARSummaryInput{AsOf: asOf, AllowedLegalEntityIDs: []string{le}})
	want := ARAging{D0To30: "3.00", D31To60: "12.00", D61To90: "48.00", D90Plus: "63.50"}
	if s.Aging != want {
		t.Fatalf("账龄分桶期望 %+v，实际 %+v", want, s.Aging)
	}
	if s.TotalReceivable != "127.00" || s.TotalReconciled != "0.50" || s.Outstanding != "126.50" {
		t.Fatalf("合计期望 127.00 / 0.50 / 126.50，实际 %s / %s / %s", s.TotalReceivable, s.TotalReconciled, s.Outstanding)
	}

	// 还没到期（到期日在统计当天之后）的也算在 ≤30 天一桶。
	future := postAR(t, r, le, customer, "0.01")
	setDueDate(t, r, future, asOf.AddDate(0, 0, 10))
	if s := summarize(t, r, ARSummaryInput{AsOf: asOf, AllowedLegalEntityIDs: []string{le}}); s.Aging.D0To30 != "3.01" {
		t.Fatalf("未到期的应收应该算在 d0_30，期望 3.01，实际 %s", s.Aging.D0To30)
	}
}

// 数据范围：只授权法人 A 的调用者，统计里只有 A 的应收；没有任何授权时全是 0。
func TestSummarizeARLedger_看不到别的法人的应收(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	a, b := newLegalEntity(t, ctx, r, "sum-scope-a"), newLegalEntity(t, ctx, r, "sum-scope-b")
	customer := uniqueID("sum-scope-cust")
	postAR(t, r, a, customer, "10.00")
	postAR(t, r, b, customer, "900.00")

	if s := summarize(t, r, ARSummaryInput{AsOf: time.Now().UTC(), AllowedLegalEntityIDs: []string{a}}); s.TotalReceivable != "10.00" {
		t.Fatalf("只授权法人 A，应收合计应该只有 A 的 10.00，实际 %s", s.TotalReceivable)
	}
	if s := summarize(t, r, ARSummaryInput{AsOf: time.Now().UTC(), AllowedLegalEntityIDs: []string{a, b}}); s.TotalReceivable != "910.00" {
		t.Fatalf("授权 A 与 B，应收合计应该是 910.00，实际 %s", s.TotalReceivable)
	}
	s := summarize(t, r, ARSummaryInput{AsOf: time.Now().UTC(), AllowedLegalEntityIDs: nil})
	zero := ARAging{D0To30: "0.00", D31To60: "0.00", D61To90: "0.00", D90Plus: "0.00"}
	if s.TotalReceivable != "0.00" || s.TotalReconciled != "0.00" || s.Outstanding != "0.00" || s.Aging != zero {
		t.Fatalf("没有任何法人授权时统计应该全是 0.00，实际 %+v", s)
	}
}

func TestSummarizeARLedger_按客户过滤(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	le := newLegalEntity(t, context.Background(), r, "sum-cust-le")
	c1, c2 := uniqueID("sum-c1"), uniqueID("sum-c2")
	postAR(t, r, le, c1, "1.50")
	postAR(t, r, le, c2, "2.50")
	if s := summarize(t, r, ARSummaryInput{CustomerID: c1, AsOf: time.Now().UTC(), AllowedLegalEntityIDs: []string{le}}); s.TotalReceivable != "1.50" {
		t.Fatalf("customer_id 过滤后应收合计应该是 1.50，实际 %s", s.TotalReceivable)
	}
}
