package repo

import (
	"context"
	"sync"
	"testing"
)

// 同一张源单的两条不同消息被并发处理（例如两个副本各收到一条、或补发的消息与原消息
// 同时到达）：只能过一次账，另一条要安全地当成重复跳过，而不是撞唯一约束报错——
// 报错的那条消息会被消费者记成失败，连同它在 event_inbox 里的记录一起回滚。
func TestPostSalesOrderEntry_同一源单并发处理只过一次账且不报错(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	in := SalesOrderEventInput{OrderID: uniqueID("race-order"), CustomerID: uniqueID("race-cust"), Amount: "10.00", EventVersion: 1}

	const n = 5
	var wg sync.WaitGroup
	dup := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dup[i], errs[i] = r.PostSalesOrderEntry(ctx, in)
		}(i)
	}
	wg.Wait()

	posted := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("第 %d 个并发处理报错（应该当成重复跳过）：%v", i, errs[i])
			continue
		}
		if !dup[i] {
			posted++
		}
	}
	if posted != 1 {
		t.Fatalf("同一源单应该恰好过账一次，实际 %d 次", posted)
	}
	var entries, ar int
	var exposure string
	if err := db.QueryRow(`SELECT count(*) FROM erp_finance.finance_journal_entries
		WHERE source_component = 'erp-sales' AND source_doc_id = $1`, in.OrderID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM erp_finance.ar_ledger WHERE customer_id = $1`, in.CustomerID).Scan(&ar); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT exposure::text FROM erp_finance.customer_credit_exposure WHERE customer_id = $1`, in.CustomerID).Scan(&exposure); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || ar != 1 || exposure != "10.00" {
		t.Fatalf("期望 1 张凭证、1 行应收、已用额度 10.00，实际 %d / %d / %s", entries, ar, exposure)
	}
}
