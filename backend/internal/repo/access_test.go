// 阶段三 Task 6：legal_entity_access 分配表 + 数据范围过滤真实生效——
// 对应 004_create_legal_entity_access.up.sql 顶部注释。
package repo

import (
	"context"
	"errors"
	"testing"
)

func TestLegalEntityAccess_grant与revoke(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	sub := uniqueID("sub-grant")

	ids, err := r.LegalEntityIDsFor(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("从没授权过应该是空列表，实际 %v", ids)
	}

	if err := r.GrantLegalEntityAccess(ctx, sub, "default"); err != nil {
		t.Fatal(err)
	}
	ids, _ = r.LegalEntityIDsFor(ctx, sub)
	if len(ids) != 1 || ids[0] != "default" {
		t.Fatalf("授权一个法人后应该只有一条，实际 %v", ids)
	}

	// 重复授权——幂等，不报错、不产生第二条。
	if err := r.GrantLegalEntityAccess(ctx, sub, "default"); err != nil {
		t.Fatalf("重复授权不该报错：%v", err)
	}
	ids, _ = r.LegalEntityIDsFor(ctx, sub)
	if len(ids) != 1 {
		t.Fatalf("重复授权同一个法人不该产生第二条，实际 %v", ids)
	}

	if err := r.GrantLegalEntityAccess(ctx, sub, "acme-hk"); err != nil {
		t.Fatal(err)
	}
	ids, _ = r.LegalEntityIDsFor(ctx, sub)
	if len(ids) != 2 {
		t.Fatalf("授权两个法人后应该有两条，实际 %v", ids)
	}

	if err := r.RevokeLegalEntityAccess(ctx, sub, "default"); err != nil {
		t.Fatal(err)
	}
	ids, _ = r.LegalEntityIDsFor(ctx, sub)
	if len(ids) != 1 || ids[0] != "acme-hk" {
		t.Fatalf("撤销 default 后应该只剩 acme-hk，实际 %v", ids)
	}

	// 撤销一条本来就不存在的分配——幂等，不报错。
	if err := r.RevokeLegalEntityAccess(ctx, sub, "default"); err != nil {
		t.Fatalf("撤销一条不存在的分配不该报错：%v", err)
	}
}

func TestLegalEntityAccess_授权空字符串拒绝(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	err := r.GrantLegalEntityAccess(ctx, uniqueID("sub-empty"), "")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("授权空字符串的法人 id 应该是 ErrInvalidArgument，实际：%v", err)
	}
}

// TestClosePeriod_没有授权时ErrForbidden 是 legal_entity 维数据权限写
// 路径的核心断言：请求体点名一个真实存在、但没有被授权访问的法人，
// 必须是 ErrForbidden，不能悄悄放行——关账是强操作，不能只靠读接口
// 的保护。
func TestClosePeriod_没有授权时ErrForbidden(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	_, err := r.ClosePeriod(ctx, PeriodOpInput{
		IdempotencyKey: uniqueID("close-forbidden"), Period: "2026-09", LegalEntityID: "default",
		AllowedLegalEntityIDs: []string{"acme-hk"}, // 只授权了另一个法人
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("没有授权的法人应该是 ErrForbidden，实际：%v", err)
	}
}

// TestPostManualEntry_没有授权时ErrForbidden 覆盖过账写路径。
func TestPostManualEntry_没有授权时ErrForbidden(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	_, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: uniqueID("post-forbidden"), LegalEntityID: "default",
		Lines:                 []Line{{AccountCode: "1122", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
		AllowedLegalEntityIDs: []string{"acme-hk"},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("没有授权的法人应该是 ErrForbidden，实际：%v", err)
	}
}

// TestReverseEntry_没有授权时ErrForbidden 覆盖冲销路径——授权校验的对象
// 是"被冲销的原凭证所属的法人"，不是调用方自己传的字段（ReverseEntryInput
// 根本没有 LegalEntityID 字段）。
func TestReverseEntry_没有授权时ErrForbidden(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	original, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: uniqueID("rev-forbidden-orig"), LegalEntityID: "default",
		Lines:                 []Line{{AccountCode: "1122", Debit: "20"}, {AccountCode: "6001", Credit: "20"}},
		AllowedLegalEntityIDs: defaultLegalEntities,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.ReverseEntry(ctx, ReverseEntryInput{
		IdempotencyKey: uniqueID("rev-forbidden"), EntryID: original.ID, Reason: "test",
		AllowedLegalEntityIDs: []string{"acme-hk"}, // 只授权了另一个法人，原凭证是 default 的
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("原凭证所属法人不在授权范围内应该是 ErrForbidden，实际：%v", err)
	}
}

// TestGetEntry_没有授权时ErrForbidden 覆盖单条读接口——不是 ErrNotFound：
// 凭证真实存在，调用者只是看不见。
func TestGetEntry_没有授权时ErrForbidden(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")

	entry, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: uniqueID("get-forbidden-orig"), LegalEntityID: "default",
		Lines:                 []Line{{AccountCode: "1122", Debit: "15"}, {AccountCode: "6001", Credit: "15"}},
		AllowedLegalEntityIDs: defaultLegalEntities,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.GetEntry(ctx, entry.ID, []string{"acme-hk"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("没有授权的法人应该是 ErrForbidden，实际：%v", err)
	}

	// 交叉验证：授权正确的法人后就能查到。
	got, err := r.GetEntry(ctx, entry.ID, defaultLegalEntities)
	if err != nil {
		t.Fatalf("授权正确的法人后应该能查到：%v", err)
	}
	if got.ID != entry.ID {
		t.Fatalf("查到的凭证 id 不对，期望 %s，实际 %s", entry.ID, got.ID)
	}
}

// TestListEntries_授权范围下推进SQL过滤 是 List 端点数据范围过滤的核心
// 断言：授权范围必须下推进 SQL 的 WHERE，不能查出全部结果后在 Go 里
// 再过滤（决策 53 的既有判据）。⚠️ 阶段二只有一个真实法人 'default'
// （没有独立的法人主数据表，见 004 迁移顶部注释）——不另造一个假法人
// 去建凭证（那会先在 lockOpenPeriodForDate 上失败，因为它没有
// accounting_periods 行），用"授权 default"与"不授权任何法人"两种
// 输入对照同一批真实数据，同样能验证过滤方向。
func TestListEntries_授权范围下推进SQL过滤(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "erp_finance_rw", "erp_finance")
	sub := uniqueID("scope-list")

	entry, err := r.PostManualEntry(ctx, PostManualEntryInput{
		IdempotencyKey: "pme-" + sub, LegalEntityID: "default",
		Lines:                 []Line{{AccountCode: "1122", Debit: "1"}, {AccountCode: "6001", Credit: "1"}},
		AllowedLegalEntityIDs: defaultLegalEntities,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.ListEntries(ctx, ListInput{PageSize: 200, AllowedLegalEntityIDs: defaultLegalEntities})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range res.Entries {
		if e.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("授权 default 后应该能在 List 结果里看到刚建的凭证 id=%s", entry.ID)
	}

	// 没有任何授权——一条都看不到，fail-closed 方向的直接验证。
	res, err = r.ListEntries(ctx, ListInput{PageSize: 200, AllowedLegalEntityIDs: nil})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 {
		t.Fatalf("没有任何授权应该一条都看不见，实际 %d 条", len(res.Entries))
	}
}
