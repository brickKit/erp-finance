package repo

import (
	"context"
	"strconv"
	"testing"
)

// TestListEntries_看不到别的法人的凭证 补 ListEntries 的真实跨法人排除
// 覆盖——access_test.go 里已有的 TestListEntries_授权范围下推进SQL过滤
// 只验证过"授权 default 后能看到自己建的凭证"+"零授权时一条都看不见"，
// 两条断言都不需要真的存在第二个法人的数据就能通过（哪怕过滤条件被
// 整个删掉，"零授权→AllowedLegalEntityIDs 传 nil→ANY('{}') 恒假"这条
// 还是会通过），"只有 default 权限时看不到别的法人凭证"这条真正的排除
// 场景此前完全没有数据能验证。
//
// 种子数据只有一个法人 "default"（defaultLegalEntities 注释），阶段二
// 没有 production 代码路径能建新法人（没有 OpenPeriod/CreatePeriod 这类
// rpc），这里用测试专属的建期间/建凭证小工具直接插两张表，不经过
// PostManualEntry——⚠️ 这个决定不是偷懒，是真的试过 PostManualEntry
// 才发现的：post_no 的格式是 `P-<period>-<seq>`（nextPostNo），不含
// legal_entity_id，但 last_post_seq 计数器却是按 (period,
// legal_entity_id) 各自独立的一行——这意味着两个法人在同一个 period
// 各自第一次过账都会生成 `P-2026-09-000001`，撞上全局唯一索引
// finance_journal_entries_post_no_uniq。这是一个真实存在、但阶段二"只
// 有一个法人"这个前提下永远不会触发的设计缺口（post_no 该不该带
// legal_entity_id 是个需要回到设计文档定的问题，这里不擅自改产品
// 代码），记入踩坑记录 C17，测试改成直接插行绕开它——本测试只关心
// ListEntries 的过滤 SQL 对不对，不需要真的走一遍过账的业务规则。
func seedPeriodForLegalEntity(t *testing.T, ctx context.Context, r *Repo, legalEntityID string) {
	t.Helper()
	// 复用 default 法人 2026-09 那个期间的 fiscal_year_id/日期范围（要
	// 真的覆盖"现在"），period 标签也复用同一个"2026-09"——finance_
	// journal_entry_lines 按 accounting_period 值 LIST 分区，只有
	// '2026-01'..'2026-12' 这 12 个分区存在，用别的标签插不进去。反正
	// 这条测试不建 entry_lines（见 seedEntryForLegalEntity），post_no
	// 冲突的坑也就不会触发。
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO erp_finance.accounting_periods (fiscal_year_id, period, legal_entity_id, start_date, end_date)
		SELECT fiscal_year_id, period, $1, start_date, end_date
		FROM erp_finance.accounting_periods WHERE legal_entity_id = 'default' AND period = '2026-09'`,
		legalEntityID)
	if err != nil {
		t.Fatalf("建测试期间失败：%v", err)
	}
}

// seedEntryForLegalEntity 直接插一行 finance_journal_entries（不经过
// PostManualEntry，也不建 entry_lines）——ListEntries 只查这张头表
// （见其实现），不需要一条完整过账凭证才能验证过滤逻辑。
func seedEntryForLegalEntity(t *testing.T, ctx context.Context, r *Repo, legalEntityID string) int64 {
	t.Helper()
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO erp_finance.finance_journal_entries (entry_no, post_no, period, legal_entity_id, status)
		VALUES ($1, $2, '2026-09', $3, 'POSTED') RETURNING id`,
		uniqueID("entry-no"), uniqueID("post-no"), legalEntityID).Scan(&id)
	if err != nil {
		t.Fatalf("建测试凭证失败：%v", err)
	}
	return id
}

func TestListEntries_看不到别的法人的凭证(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	otherEntity := uniqueID("acme-other")
	seedPeriodForLegalEntity(t, ctx, r, otherEntity)

	mineID := strconv.FormatInt(seedEntryForLegalEntity(t, ctx, r, "default"), 10)
	otherID := strconv.FormatInt(seedEntryForLegalEntity(t, ctx, r, otherEntity), 10)

	// 只授权 default——应该能看到自己法人的凭证，看不到另一个真实存在
	// 的法人的凭证。
	res, err := r.ListEntries(ctx, ListInput{PageSize: 200, AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	foundMine, foundOther := false, false
	for _, e := range res.Entries {
		if e.ID == mineID {
			foundMine = true
		}
		if e.ID == otherID {
			foundOther = true
		}
	}
	if !foundMine {
		t.Fatal("授权 default 后应该能看到自己法人的凭证")
	}
	if foundOther {
		t.Fatal("授权 default 不该看到另一个真实存在的法人的凭证")
	}
}
