package repo

import (
	"context"
	"testing"
)

// newLegalEntity 造一个全新的法人：照 default 法人的 FY2026 建 12 个会计期间
// （状态一律 OPEN，不照抄 default 当前的状态）。法人没有主数据表，期间就是它
// 在本组件里存在的全部痕迹；每次测试用一个新 id，结果不受测试库里旧数据影响。
func newLegalEntity(t *testing.T, ctx context.Context, r *Repo, prefix string) string {
	t.Helper()
	le := uniqueID(prefix)
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO erp_finance.accounting_periods (fiscal_year_id, period, legal_entity_id, start_date, end_date)
		SELECT fiscal_year_id, period, $1, start_date, end_date
		FROM erp_finance.accounting_periods WHERE legal_entity_id = 'default'`, le); err != nil {
		t.Fatalf("建测试法人的会计期间失败：%v", err)
	}
	return le
}

// 每个法人各有一套账：post_no 在同一法人、同一期间内连续，不同法人在同一期间
// 各自从 1 编号。两个法人在同一期间各自第一次过账都必须成功。
func TestPostManualEntry_两个法人同一期间各自从1编号(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	a := newLegalEntity(t, ctx, r, "le-a")
	b := newLegalEntity(t, ctx, r, "le-b")

	var postNos []string
	for _, le := range []string{a, b} {
		e, err := r.PostManualEntry(ctx, PostManualEntryInput{
			IdempotencyKey: uniqueID("two-entities"), LegalEntityID: le, AllowedLegalEntityIDs: []string{a, b},
			Lines: []Line{{AccountCode: "1405", Debit: "1.00"}, {AccountCode: "2202", Credit: "1.00"}},
		})
		if err != nil {
			t.Fatalf("法人 %s 第一次过账失败：%v", le, err)
		}
		postNos = append(postNos, e.PostNo)
	}
	if postNos[0] != postNos[1] {
		t.Fatalf("两个法人同一期间的第一张凭证都应该是 1 号，实际 %v", postNos)
	}
}
