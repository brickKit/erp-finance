package consumer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/backend/internal/repo"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
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

func natsURLForTest(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

// testSubject 给消费者测试造一个测试私有的 subject，不直接用生产真实
// subject。
//
// ⚠️ 实测踩坑（docs/dev/field-tested-pitfalls-log.md 类别 E 的 E2）：这几条
// 测试原来直接订阅/发布到真实 subject（如 "sales.order.created.v1"），
// 而同一台机器上 `brickkit up` 真实跑着的 erp-finance 容器订阅的是
// **同一个** subject——NATS 核心发布订阅对同一 subject 的多个订阅者是
// 广播，两边都会收到测试发布的消息，谁先把 event_inbox 那一行 INSERT
// 成功谁就真正执行 handler，断言读到的可能是真实容器的产出，不是本地
// 被测代码的产出。
//
// 换一个测试私有的 subject 就能让真实容器完全收不到——它们只订阅生产
// subject 字面量，不会去猜一个带随机后缀的名字。这个换法是安全的：
// besdk.Consume 的 fn 只用 ev.Subject 拼错误信息，不拿它做任何业务判断，
// 换成任意字符串不影响被测逻辑本身。这条规避法只适用于"测试直接构造/
// 发布事件"的消费者测试——验证"真的发到了生产 subject 上"这件事本身的
// 测试必须用真实 subject，不适用这个换法（本文件没有这类测试）。
func testSubject(base string) string {
	return fmt.Sprintf("test.%s.%d", base, time.Now().UnixNano())
}

// publishEvent 的 aggregateID 必须是每次调用都不同的值——besdk.Consume
// 的 event_inbox 按 (subject, aggregate_id, version) 做单调去重，且这张
// 表是持久化的（不会在两次 go test 之间清空）。之前踩过这个坑：所有
// 测试共用固定的 "test-agg"，第二次跑测试套件时 version=1 已经在
// event_inbox 里出现过，Consume 静默跳过、handler 根本没被调用，断言
// 读到的是"事件从没处理过"的初始状态而不是真的失败。
func publishEvent(t *testing.T, nc *nats.Conn, subject, aggregateID string, version int64, payload string) {
	t.Helper()
	msg := &nats.Msg{Subject: subject, Data: []byte(payload), Header: nats.Header{}}
	msg.Header.Set("X-Aggregate-Id", aggregateID)
	msg.Header.Set("X-Version", strconv.FormatInt(version, 10))
	msg.Header.Set("X-Hop-Count", "0")
	if err := nc.PublishMsg(msg); err != nil {
		t.Fatal(err)
	}
}

// TestConsumer_销售订单事件生成应收凭证 是消费面最重那条链路
// （sales.order.created.v1 → 应收凭证 + AR 台账 + 已用额度）的端到端
// 测试：真订阅、真发布、真等待，不是直接调 repo 函数。
func TestConsumer_销售订单事件生成应收凭证(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	orderID := fmt.Sprintf("consumer-order-%d", time.Now().UnixNano())
	customerID := fmt.Sprintf("consumer-cust-%d", time.Now().UnixNano())
	subj := testSubject("sales.order.created.v1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = besdk.Consume(ctx, nc, db, "erp_finance_rw", "erp_finance", subj,
			salesOrderHandler(slog.Default()))
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)

	payload := fmt.Sprintf(`{"order_id":%q,"customer_id":%q,"total_amount":"250.00"}`, orderID, customerID)
	publishEvent(t, nc, subj, orderID, 1, payload)
	nc.Flush()
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done

	r := repo.New(db, "erp_finance_rw", "erp_finance")
	ce, err := r.GetCreditExposure(context.Background(), customerID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.Exposure != "250.00" {
		t.Fatalf("期望已用额度 250.00，实际 %q", ce.Exposure)
	}

	arRes, err := r.ListARLedger(context.Background(), repo.ListARLedgerInput{
		CustomerID: customerID, PageSize: 10, AllowedLegalEntityIDs: []string{"default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arRes.Entries) != 1 {
		t.Fatalf("期望 1 条 AR 台账，实际 %d 条", len(arRes.Entries))
	}
}

// TestConsumer_库存调整事件生成存货凭证 验证消费 erp.inventory.adjusted.v1
// 这条链路——真订阅、真发布、真等待。
func TestConsumer_库存调整事件生成存货凭证(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	movementID := fmt.Sprintf("consumer-movement-%d", time.Now().UnixNano())
	subj := testSubject("erp.inventory.adjusted.v1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = besdk.Consume(ctx, nc, db, "erp_finance_rw", "erp_finance", subj,
			inventoryAdjustedHandler(slog.Default()))
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)

	payload := fmt.Sprintf(`{"product_id":"P-1","warehouse_id":"1","qty_delta":"5","movement_id":%q,"reason":"RECEIVE"}`, movementID)
	publishEvent(t, nc, subj, movementID, 1, payload)
	nc.Flush()
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done

	var n int
	if err := besdk.WithTx(context.Background(), db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM finance_journal_entries
			WHERE source_component = 'erp-inventory' AND source_doc_id = $1`, movementID).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("期望生成 1 条凭证，实际 %d 条", n)
	}
}

// TestConsumer_客户事件维护信用额度摘要副本 验证消费
// mdm.customer.created.v1 这条链路。
func TestConsumer_客户事件维护信用额度摘要副本(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	customerID := fmt.Sprintf("consumer-snapshot-%d", time.Now().UnixNano())
	subj := testSubject("mdm.customer.created.v1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = besdk.Consume(ctx, nc, db, "erp_finance_rw", "erp_finance", subj,
			customerSnapshotHandler())
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)

	payload := fmt.Sprintf(`{"id":%q,"credit_limit":"8000.00"}`, customerID)
	publishEvent(t, nc, subj, customerID, 1, payload)
	nc.Flush()
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done

	var limit string
	if err := besdk.WithTx(context.Background(), db, "erp_finance_rw", "erp_finance", func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT credit_limit FROM customer_credit_snapshots WHERE customer_id = $1`,
			customerID).Scan(&limit)
	}); err != nil {
		t.Fatal(err)
	}
	if limit != "8000.00" {
		t.Fatalf("期望 credit_limit=8000.00，实际 %q", limit)
	}
}
