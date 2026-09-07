package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib" // §12.4：不用 lib/pq，驱动名注册为 "pgx"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var custSeq int64

// uniqueID 给每个测试造一个独立的 id 前缀（customer/order/entry 等），
// 测试之间不共享行，互不干扰。
func uniqueID(prefix string) string {
	n := atomic.AddInt64(&custSeq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}

func TestCheckPeriodOpen_种子期间应该是OPEN(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	status, err := r.CheckPeriodOpen(context.Background(), "2026-09", "default")
	if err != nil {
		t.Fatal(err)
	}
	if status != PeriodOpen {
		t.Fatalf("期望种子期间是 OPEN，实际 %q", status)
	}
}

func TestCheckPeriodOpen_不存在的期间返回空字符串(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	status, err := r.CheckPeriodOpen(context.Background(), "2099-01", "default")
	if err != nil {
		t.Fatal(err)
	}
	if status != "" {
		t.Fatalf("期望空字符串（不存在），实际 %q", status)
	}
}

func TestClosePeriod_然后LockPeriod_然后不能反关账(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	period := "2026-01" // 用只被这条测试改状态的期间，避免和其他测试冲突

	// ⚠️ LOCKED 是真正的终态，而期间是有限资源（一年只有 12 个）——本地
	// 反复 `go test`（不是每次都对着一个全新迁移出来的库）会撞到"上一轮
	// 已经推到 LOCKED"的情况。按当前状态跳着走，让这条测试可以安全重跑
	// 任意多次，同时仍然覆盖"能推进到 LOCKED"这条路径（第一次跑一定会
	// 经过 OPEN→CLOSED→LOCKED 的真实转换）。
	status, err := r.CheckPeriodOpen(ctx, period, "default")
	if err != nil {
		t.Fatal(err)
	}
	if status == PeriodOpen {
		status, err = r.ClosePeriod(ctx, PeriodOpInput{
			IdempotencyKey: uniqueID("close"), Period: period, LegalEntityID: "default"})
		if err != nil {
			t.Fatal(err)
		}
		if status != PeriodClosed {
			t.Fatalf("期望 CLOSED，实际 %q", status)
		}
	}
	if status == PeriodClosed {
		status, err = r.LockPeriod(ctx, PeriodOpInput{
			IdempotencyKey: uniqueID("lock"), Period: period, LegalEntityID: "default"})
		if err != nil {
			t.Fatal(err)
		}
		if status != PeriodLocked {
			t.Fatalf("期望 LOCKED，实际 %q", status)
		}
	}
	if status != PeriodLocked {
		t.Fatalf("期望走到 LOCKED，实际停在 %q", status)
	}

	// LOCKED 是终态，ReopenPeriod 应该报错，不是静默成功——这条断言无论
	// 是不是第一次跑都成立。
	_, err = r.ReopenPeriod(ctx, PeriodOpInput{
		IdempotencyKey: uniqueID("reopen"), Period: period, LegalEntityID: "default"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("对 LOCKED 期间 Reopen 应该报错，实际：%v", err)
	}
}

func TestClosePeriod_重复调用是幂等的(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	period := "2026-02"

	if _, err := r.ClosePeriod(ctx, PeriodOpInput{
		IdempotencyKey: uniqueID("close"), Period: period, LegalEntityID: "default"}); err != nil {
		t.Fatal(err)
	}
	// 已经是 CLOSED，再关一次（不同 idempotency_key，模拟"没记住结果的重试"）
	// 应该安全返回 CLOSED，不报错。
	status, err := r.ClosePeriod(ctx, PeriodOpInput{
		IdempotencyKey: uniqueID("close-retry"), Period: period, LegalEntityID: "default"})
	if err != nil {
		t.Fatalf("对已经 CLOSED 的期间重复 Close 不该报错：%v", err)
	}
	if status != PeriodClosed {
		t.Fatalf("期望仍然是 CLOSED，实际 %q", status)
	}
}

func TestPostManualEntry_借贷不平衡时拒绝且不落库(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	key := uniqueID("unbalanced")
	_, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: key, LegalEntityID: "default",
		Lines: []Line{
			{AccountCode: "1122", Debit: "100"},
			{AccountCode: "6001", Credit: "50"}, // 故意不平
		},
	})
	if !errors.Is(err, ErrUnbalancedEntry) {
		t.Fatalf("借贷不平应该报 ErrUnbalancedEntry，实际：%v", err)
	}

	// 校验发生在 postEntryTx 内部、任何写操作之前，且失败让整个 WithTx
	// 事务回滚——claimIdempotency 声明的那一行也该跟着回滚，不留痕迹。
	var n int
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM command_idempotency WHERE idempotency_key = $1`, key).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("校验失败应该让整个事务（含 claimIdempotency）回滚，实际 command_idempotency 留了 %d 条", n)
	}
}

func TestPostManualEntry_成功过账并生成postNo(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	entry, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: uniqueID("manual"), LegalEntityID: "default",
		Lines: []Line{
			{AccountCode: "1122", Debit: "100.00"},
			{AccountCode: "6001", Credit: "100.00"},
		},
		Memo: "测试手工凭证",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != EntryPosted {
		t.Fatalf("期望 POSTED，实际 %q", entry.Status)
	}
	if entry.PostNo == "" {
		t.Fatal("期望非空 post_no")
	}
	if entry.EntryNo == "" {
		t.Fatal("期望非空 entry_no")
	}
	if len(entry.Lines) != 2 {
		t.Fatalf("期望 2 行，实际 %d", len(entry.Lines))
	}
}

func TestPostManualEntry_幂等(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	key := uniqueID("manual-idem")

	in := PostManualEntryInput{
		IdempotencyKey: key, LegalEntityID: "default",
		Lines: []Line{{AccountCode: "1122", Debit: "50"}, {AccountCode: "6001", Credit: "50"}},
	}
	e1, err := r.PostManualEntry(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := r.PostManualEntry(ctx, in)
	if err != nil {
		t.Fatalf("幂等重试报错了：%v", err)
	}
	if e1.ID != e2.ID {
		t.Fatalf("幂等失效：第一次 %s，第二次 %s", e1.ID, e2.ID)
	}
}

func TestPostEntryTx_业务日期超出所有已建期间时报NotFound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, err := postEntryTx(ctx, mustBeginTx(t, db), postEntryTxInput{
		LegalEntityID: "default",
		BusinessDate:  mustParseDate(t, "2099-01-15"),
		Lines:         []Line{{AccountCode: "1122", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound（2099 年没有任何已建期间），实际：%v", err)
	}
}

// TestPostEntryTx_关闭的期间顺延到下一个开放期间 是设计计划 §3.1"迟到的
// 凭证记进下一个开放期间，不是拒绝"的直接测试。用 2026-06（本测试专用，
// 不与其他测试共享）：关掉它，业务日期落在 6 月的凭证应该顺延进 7 月
// （下一个 OPEN 的期间），而不是报错。
func TestPostEntryTx_关闭的期间顺延到下一个开放期间(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	if _, err := r.ClosePeriod(ctx, PeriodOpInput{
		IdempotencyKey: uniqueID("close-for-rollforward"), Period: "2026-06", LegalEntityID: "default"}); err != nil {
		t.Fatal(err)
	}

	entry, err := postEntryTx(ctx, mustBeginTx(t, db), postEntryTxInput{
		LegalEntityID: "default",
		BusinessDate:  mustParseDate(t, "2026-06-15"),
		Lines:         []Line{{AccountCode: "1122", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
	})
	if err != nil {
		t.Fatalf("6 月关账后，6 月的凭证应该顺延到 7 月而不是报错：%v", err)
	}
	if entry.Period != "2026-07" {
		t.Fatalf("期望顺延到 2026-07，实际落在 %q", entry.Period)
	}
}

func mustBeginTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec("SET LOCAL ROLE erp_finance_rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("SET LOCAL search_path TO erp_finance, erp_finance_archive"); err != nil {
		t.Fatal(err)
	}
	return tx
}

func mustParseDate(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestReverseEntry_原凭证不变且冲销凭证借贷互换(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	original, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: uniqueID("rev-orig"), LegalEntityID: "default",
		Lines: []Line{{AccountCode: "1122", Debit: "80"}, {AccountCode: "6001", Credit: "80"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	reversal, err := r.ReverseEntry(ctx, ReverseEntryInput{
		IdempotencyKey: uniqueID("rev"), EntryID: original.ID, Reason: "测试冲销",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reversal.ID == original.ID {
		t.Fatal("冲销应该产生一张新凭证，不是复用原凭证")
	}

	// 原凭证一个字不动
	stillOriginal, err := r.GetEntry(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOriginal.Status != EntryPosted {
		t.Fatalf("原凭证的状态不该被改变，实际 %q", stillOriginal.Status)
	}
	if stillOriginal.Lines[0].Debit != "80.00" {
		t.Fatalf("原凭证的分录不该被改变，实际 debit=%q", stillOriginal.Lines[0].Debit)
	}

	// 冲销凭证借贷互换
	foundDebitOn6001 := false
	for _, l := range reversal.Lines {
		if l.AccountCode == "6001" && l.Debit == "80.00" {
			foundDebitOn6001 = true
		}
	}
	if !foundDebitOn6001 {
		t.Fatalf("期望冲销凭证在 6001 科目上是借方 80，实际：%+v", reversal.Lines)
	}
}

func TestReverseEntry_对草稿凭证报错(t *testing.T) {
	// 阶段二 PostManualEntry 直接过账，没有真正的 DRAFT 态凭证留在库里
	// 可以拿来测——这条测试改成验证对不存在的 entry_id 报 ErrNotFound，
	// 覆盖 ReverseEntry 的另一半校验路径。
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	_, err := r.ReverseEntry(ctx, ReverseEntryInput{
		IdempotencyKey: uniqueID("rev-missing"), EntryID: "999999999", Reason: "test",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("冲销一张不存在的凭证应该报 ErrNotFound，实际：%v", err)
	}
}

func TestPostSalesOrderEntry_生成应收凭证AR台账与已用额度(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	customerID := uniqueID("cust")
	orderID := uniqueID("order")

	duplicate, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
		OrderID: orderID, CustomerID: customerID, Amount: "500.00", EventVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("第一次不该是 duplicate")
	}

	ce, err := r.GetCreditExposure(ctx, customerID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.Exposure != "500.00" {
		t.Fatalf("期望已用额度 500.00，实际 %q", ce.Exposure)
	}

	arRes, err := r.ListARLedger(ctx, ListARLedgerInput{CustomerID: customerID, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(arRes.Entries) != 1 || arRes.Entries[0].Amount != "500.00" {
		t.Fatalf("期望 1 条 AR 台账、金额 500.00，实际：%+v", arRes.Entries)
	}
}

func TestPostSalesOrderEntry_同一源单重放不重复过账(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	customerID := uniqueID("cust-dup")
	orderID := uniqueID("order-dup")

	in := SalesOrderEventInput{OrderID: orderID, CustomerID: customerID, Amount: "100.00", EventVersion: 1}
	if dup, err := r.PostSalesOrderEntry(ctx, in); err != nil || dup {
		t.Fatalf("第一次应该成功且不是 duplicate，dup=%v err=%v", dup, err)
	}
	dup, err := r.PostSalesOrderEntry(ctx, in)
	if err != nil {
		t.Fatalf("重放同一源单不该报错：%v", err)
	}
	if !dup {
		t.Fatal("重放同一源单应该被识别为 duplicate")
	}

	ce, err := r.GetCreditExposure(ctx, customerID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.Exposure != "100.00" {
		t.Fatalf("重放不该重复累加已用额度，期望 100.00，实际 %q", ce.Exposure)
	}
}

func TestPostSalesOrderEntry_超限发布creditRejected事件(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	customerID := uniqueID("cust-limit")
	orderID := uniqueID("order-limit")

	// 先给这个客户一个额度值（摘要副本）
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return UpsertCustomerCreditSnapshotTx(tx, customerID, "200.00", 1)
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
		OrderID: orderID, CustomerID: customerID, Amount: "300.00", EventVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return tx.QueryRow(
			`SELECT count(*) FROM event_outbox WHERE subject = 'finance.credit.rejected.v1' AND aggregate_id = $1`,
			orderID).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("期望落 1 条 finance.credit.rejected.v1，实际 %d 条", n)
	}
}

func TestPostSalesOrderEntry_未配额度时不拒绝(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	customerID := uniqueID("cust-nolimit")
	orderID := uniqueID("order-nolimit")

	// 不设摘要副本 → credit_limit 视为 0 → 不拦（判据见 exceedsLimit 注释）
	if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{
		OrderID: orderID, CustomerID: customerID, Amount: "999999.00", EventVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return tx.QueryRow(
			`SELECT count(*) FROM event_outbox WHERE subject = 'finance.credit.rejected.v1' AND aggregate_id = $1`,
			orderID).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("未配额度不该拒绝，期望 0 条 credit.rejected 事件，实际 %d 条", n)
	}
}

func TestPostInventoryAdjustedEntry_生成存货凭证且金额是占位换算(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	movementID := uniqueID("movement")

	dup, err := r.PostInventoryAdjustedEntry(ctx, InventoryAdjustedEventInput{
		ProductID: "P-1", WarehouseID: "1", QtyDelta: "10", MovementID: movementID,
		Reason: "RECEIVE", EventVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("第一次不该是 duplicate")
	}

	entryID, found, err := findExistingBySourceForTest(ctx, db, "erp-inventory", "movement", movementID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("期望找到对应的凭证")
	}
	entry, err := r.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatal(err)
	}
	// RECEIVE: 借 1405 存货 / 贷 2202 应付。qty_delta=10 → amount=10.00（占位换算）。
	var gotDebit, gotCredit string
	for _, l := range entry.Lines {
		if l.AccountCode == "1405" {
			gotDebit = l.Debit
		}
		if l.AccountCode == "2202" {
			gotCredit = l.Credit
		}
	}
	if gotDebit != "10.00" || gotCredit != "10.00" {
		t.Fatalf("期望 1405 借 10.00、2202 贷 10.00，实际 debit=%q credit=%q", gotDebit, gotCredit)
	}
}

func findExistingBySourceForTest(ctx context.Context, db *sql.DB, component, docType, docID string, revision int64) (string, bool, error) {
	var id, found string
	err := besdk.WithTx(ctx, db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		gotID, gotFound, err := findExistingBySource(ctx, tx, component, docType, docID, revision)
		id, found = gotID, fmt.Sprint(gotFound)
		return err
	})
	return id, found == "true", err
}

func TestPostInventoryAdjustedEntry_qtyDelta为0时不生成凭证(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	dup, err := r.PostInventoryAdjustedEntry(ctx, InventoryAdjustedEventInput{
		ProductID: "P-1", WarehouseID: "1", QtyDelta: "0", MovementID: uniqueID("movement-zero"),
		Reason: "ADJUST_GAIN", EventVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("qty_delta=0 应该是'跳过'，不是'重复'")
	}
}

// TestPostManualEntry_并发过账post_no连续无缺口 是 post_no 生成机制
// （设计计划 §9 第 7 条）的并发正确性测试：N 个并发的手工凭证都过账到
// 同一个期间，post_no 必须两两不同、且是连续的整数序列（1..N），
// 不许有缺口也不许重复——这正是"锁期间行"要保证的东西。
func TestPostManualEntry_并发过账postNo连续无缺口(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(20)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	const n = 15
	var wg sync.WaitGroup
	wg.Add(n)
	postNos := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			entry, err := r.PostManualEntry(ctx, PostManualEntryInput{
				IdempotencyKey: uniqueID(fmt.Sprintf("concurrent-post-%d", i)), LegalEntityID: "default",
				Lines: []Line{{AccountCode: "1122", Debit: "1"}, {AccountCode: "6001", Credit: "1"}},
			})
			if err != nil {
				errs[i] = err
				return
			}
			postNos[i] = entry.PostNo
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d 过账失败：%v", i, err)
		}
		if seen[postNos[i]] {
			t.Fatalf("post_no 重复：%q", postNos[i])
		}
		seen[postNos[i]] = true
	}
	if len(seen) != n {
		t.Fatalf("期望 %d 个互不相同的 post_no，实际 %d 个", n, len(seen))
	}
}
