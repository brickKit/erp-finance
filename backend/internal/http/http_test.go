package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
	"github.com/brickKit/erp-finance/v2/backend/internal/service"
)

// 这里测的是 REST 层把查询参数接对了、响应字段写对了：handler 挂在测试自己的
// engine 上，权限判定（RequirePermission）不在这里测——那是 SDK 的事。
// 中间件只做一件事：把一份已验签的 Claims 放进 ctx，service 层据此查调用者的
// legal_entity_access。

const schema, role = "erp_finance", "erp_finance_rw"

func testRepo(t *testing.T) (*repo.Repo, *sql.DB) {
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
	return repo.New(db, role, schema), db
}

var seq int64

func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(&seq, 1))
}

// newSub 造一个只能访问 legalEntityIDs 的调用者。
func newSub(t *testing.T, r *repo.Repo, legalEntityIDs ...string) string {
	t.Helper()
	sub := uniqueID("http-sub")
	for _, le := range legalEntityIDs {
		if err := r.GrantLegalEntityAccess(context.Background(), sub, le); err != nil {
			t.Fatal(err)
		}
	}
	return sub
}

// get 以 sub 的身份请求 GET path?query，返回状态码与解析后的 JSON。
func get(t *testing.T, r *repo.Repo, sub, path string, query url.Values) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(besdk.ContextWithClaims(c.Request.Context(), besdk.Claims{Sub: sub}))
	})
	svc := service.New(r, slog.Default())
	g := eng.Group("/erp/finance")
	g.GET("/entries", listEntriesHandler(svc))
	g.GET("/ar-ledger", listARLedgerHandler(svc))
	g.GET("/ar-ledger/summary", arLedgerSummaryHandler(svc))
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/erp/finance"+path+"?"+query.Encode(), nil))
	var body map[string]any
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON：%v（%s）", err, w.Body.String())
		}
	}
	return w.Code, body
}

func ids(t *testing.T, body map[string]any, key string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	items, _ := body[key].([]any)
	for _, it := range items {
		out[it.(map[string]any)["id"].(string)] = true
	}
	return out
}

func backdate(t *testing.T, db *sql.DB, table, id string, age time.Duration) {
	t.Helper()
	err := besdk.WithTx(context.Background(), db, role, schema, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE `+table+` SET created_at = $1 WHERE id = $2`, time.Now().Add(-age), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 契约里 GET /entries 带 created_after / created_before，不传时默认最近 90 天。
// 一张 200 天前的凭证默认查不到，给了 created_after 就必须查得到。
func TestListEntries_REST按created_after放宽默认时间窗口(t *testing.T) {
	r, db := testRepo(t)
	ctx := context.Background()
	e, err := r.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: uniqueID("http-entry"), LegalEntityID: "default", AllowedLegalEntityIDs: []string{"default"},
		Lines: []repo.Line{{AccountCode: "1405", Debit: "1.00"}, {AccountCode: "2202", Credit: "1.00"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, db, "finance_journal_entries", e.ID, 200*24*time.Hour)
	sub := newSub(t, r, "default")

	code, body := get(t, r, sub, "/entries", url.Values{"page_size": {"200"}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if ids(t, body, "entries")[e.ID] {
		t.Fatal("200 天前的凭证不该出现在默认 90 天窗口里")
	}

	code, body = get(t, r, sub, "/entries", url.Values{
		"page_size":      {"200"},
		"created_after":  {time.Now().Add(-201 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		"created_before": {time.Now().Add(-199 * 24 * time.Hour).UTC().Format(time.RFC3339)},
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if !ids(t, body, "entries")[e.ID] {
		t.Fatal("给了 created_after / created_before，200 天前的凭证应该在结果里")
	}
}

func TestListEntries_REST的created_after不是RFC3339时返回400(t *testing.T) {
	r, _ := testRepo(t)
	sub := newSub(t, r, "default")
	code, _ := get(t, r, sub, "/entries", url.Values{"created_after": {"昨天"}})
	if code != http.StatusBadRequest {
		t.Fatalf("created_after 不是 RFC 3339 时间应返回 400，实际 %d", code)
	}
}

func TestListARLedger_REST按created_after放宽默认时间窗口(t *testing.T) {
	r, db := testRepo(t)
	ctx := context.Background()
	customer := uniqueID("http-cust")
	if _, err := r.PostSalesOrderEntry(ctx, repo.SalesOrderEventInput{
		OrderID: uniqueID("http-order"), CustomerID: customer, Amount: "10.00", EventVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	sub := newSub(t, r, "default")
	code, body := get(t, r, sub, "/ar-ledger", url.Values{"customer_id": {customer}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	var arID string
	for id := range ids(t, body, "entries") {
		arID = id
	}
	if arID == "" {
		t.Fatal("刚写入的应收行应该在默认窗口里")
	}
	backdate(t, db, "ar_ledger", arID, 200*24*time.Hour)

	if _, body = get(t, r, sub, "/ar-ledger", url.Values{"customer_id": {customer}}); ids(t, body, "entries")[arID] {
		t.Fatal("200 天前的应收行不该出现在默认 90 天窗口里")
	}
	code, body = get(t, r, sub, "/ar-ledger", url.Values{
		"customer_id":   {customer},
		"created_after": {time.Now().Add(-201 * 24 * time.Hour).UTC().Format(time.RFC3339)},
	})
	if code != http.StatusOK || !ids(t, body, "entries")[arID] {
		t.Fatalf("给了 created_after，200 天前的应收行应该在结果里（状态 %d）", code)
	}
	if code, _ := get(t, r, sub, "/ar-ledger", url.Values{"created_before": {"2026-13-01"}}); code != http.StatusBadRequest {
		t.Fatalf("created_before 不是 RFC 3339 时间应返回 400，实际 %d", code)
	}
}

func TestListEntries_REST透传source_doc_id与source_doc_type(t *testing.T) {
	r, _ := testRepo(t)
	ctx := context.Background()
	order := uniqueID("http-src-order")
	if _, err := r.PostSalesOrderEntry(ctx, repo.SalesOrderEventInput{OrderID: order, CustomerID: uniqueID("http-src-cust"), Amount: "2.00", EventVersion: 1}); err != nil {
		t.Fatal(err)
	}
	sub := newSub(t, r, "default")
	code, body := get(t, r, sub, "/entries", url.Values{"source_doc_id": {order}, "source_doc_type": {"order"}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	items, _ := body["entries"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["source_doc_id"] != order {
		t.Fatalf("按 source_doc_id 过滤应该恰好得到 1 张凭证，实际 %d 张", len(items))
	}
}

func TestListARLedger_REST返回客户名未核销余额与到期日(t *testing.T) {
	r, db := testRepo(t)
	ctx := context.Background()
	customer := uniqueID("http-ar-name")
	if err := besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		return repo.UpsertCustomerSnapshotTx(tx, customer, "「本地测试」华北贸易", "0", 1, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PostSalesOrderEntry(ctx, repo.SalesOrderEventInput{OrderID: uniqueID("http-ar-order"), CustomerID: customer, Amount: "12.34", EventVersion: 1}); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, r, newSub(t, r, "default"), "/ar-ledger", url.Values{"customer_id": {customer}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	items, _ := body["entries"].([]any)
	if len(items) != 1 {
		t.Fatalf("期望 1 行，实际 %d", len(items))
	}
	row := items[0].(map[string]any)
	if row["customer_name"] != "「本地测试」华北贸易" || row["outstanding"] != "12.34" || row["due_date"] != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("customer_name / outstanding / due_date 不对：%v", row)
	}
}

// GET /ar-ledger/summary：金额是字符串、账龄四个桶都在、按调用者的法人授权过滤、
// customer_id 透传。
func TestARLedgerSummary_REST(t *testing.T) {
	r, _ := testRepo(t)
	ctx := context.Background()
	customer := uniqueID("http-sum-cust")
	if _, err := r.PostSalesOrderEntry(ctx, repo.SalesOrderEventInput{OrderID: uniqueID("http-sum-order"), CustomerID: customer, Amount: "7.25", EventVersion: 1}); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, r, newSub(t, r, "default"), "/ar-ledger/summary", url.Values{"customer_id": {customer}})
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	aging, _ := body["aging"].(map[string]any)
	if body["total_receivable"] != "7.25" || body["total_reconciled"] != "0.00" || body["outstanding"] != "7.25" ||
		aging["d0_30"] != "7.25" || aging["d31_60"] != "0.00" || aging["d61_90"] != "0.00" || aging["d90_plus"] != "0.00" ||
		body["as_of"] != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("统计结果不对：%v", body)
	}

	// 没有 default 法人授权的调用者：同一个客户的应收一分都看不到。
	_, body = get(t, r, newSub(t, r), "/ar-ledger/summary", url.Values{"customer_id": {customer}})
	if body["total_receivable"] != "0.00" {
		t.Fatalf("没有法人授权时应收合计应该是 0.00，实际 %v", body["total_receivable"])
	}
}
