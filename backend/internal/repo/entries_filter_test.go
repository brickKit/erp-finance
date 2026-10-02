package repo

import (
	"context"
	"testing"
)

// GET /entries?source_doc_id=&source_doc_type=：订单详情页"查看对应凭证"按源单反查。
func TestListEntries_按源单过滤(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	orderA, orderB := uniqueID("flt-order-a"), uniqueID("flt-order-b")
	for _, o := range []string{orderA, orderB} {
		if _, err := r.PostSalesOrderEntry(ctx, SalesOrderEventInput{OrderID: o, CustomerID: uniqueID("flt-cust"), Amount: "3.00", EventVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := r.ListEntries(ctx, ListInput{PageSize: 200, SourceDocID: orderA, SourceDocType: "order", AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].SourceDocID != orderA {
		t.Fatalf("按 source_doc_id=%s 过滤应该恰好得到它那一张凭证，实际 %d 张", orderA, len(res.Entries))
	}

	res, err = r.ListEntries(ctx, ListInput{PageSize: 200, SourceDocID: orderA, SourceDocType: "movement", AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 {
		t.Fatalf("source_doc_type 不同就不该匹配，实际 %d 张", len(res.Entries))
	}

	// 只给类型：同类型的都在，别的类型都不在。
	res, err = r.ListEntries(ctx, ListInput{PageSize: 200, SourceDocType: "order", AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range res.Entries {
		if e.SourceDocType != "order" {
			t.Fatalf("source_doc_type=order 的结果里出现了 %q", e.SourceDocType)
		}
		seen[e.SourceDocID] = true
	}
	if !seen[orderA] || !seen[orderB] {
		t.Fatal("source_doc_type=order 应该包含刚建的两张订单凭证")
	}
}
