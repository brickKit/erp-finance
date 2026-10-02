package repo

import (
	"context"
	"database/sql"
	"testing"
)

// PG_SCHEMA 可以配成别的名字（例如同一个库里装两份财务）：事件必须写进本组件
// 当前事务所在 schema 的 event_outbox——Outbox 推送线程只读那一张；写进写死的
// erp_finance，事件就永远发不出去（或者 schema 不存在时直接报错）。
func TestPublishVoucherPosted_写进当前事务所在schema的outbox(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	const alt = "erp_finance_cfgtest"
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS ` + alt + ` CASCADE`,
		`CREATE SCHEMA ` + alt,
		`CREATE TABLE ` + alt + `.event_outbox (LIKE erp_finance.event_outbox INCLUDING DEFAULTS)`,
		`GRANT USAGE ON SCHEMA ` + alt + ` TO erp_finance_rw`,
		`GRANT SELECT, INSERT ON ` + alt + `.event_outbox TO erp_finance_rw`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s：%v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + alt + ` CASCADE`) })

	entryID := uniqueID("cfg-entry")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{"SET LOCAL ROLE erp_finance_rw", "SET LOCAL search_path TO " + alt + ", erp_finance"} {
		if _, err := tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishVoucherPosted(tx, entryID, "E-1", "P-1", "2026-10", "1.00"); err != nil {
		t.Fatal(err)
	}
	count := func(schema string) int {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM `+schema+`.event_outbox WHERE aggregate_id = $1`, entryID).Scan(&n); err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		return n
	}
	if got, wrong := count(alt), count("erp_finance"); got != 1 || wrong != 0 {
		t.Fatalf("事件应该写进 %s.event_outbox（实际 %d 条），不该写进 erp_finance（实际 %d 条）", alt, got, wrong)
	}
}
