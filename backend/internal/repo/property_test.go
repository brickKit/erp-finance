package repo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

// accountCodes 是 003_seed_accounts_and_periods.up.sql 种下的 5 个科目，
// 属性测试用它们随机拼分录行。
var accountCodes = []string{"1122", "2202", "1405", "6001", "6401"}

func centsToDecimal(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// TestProperty_借贷不平衡的分录被拒绝且不落库 是 04-testing-standard.md §3.2
// 对"核心交易类组件"（资金）的强制要求：复式记账"借贷必须相等"是这个
// 组件唯一、也是最重要的不变式，此前只有例子测试
// （TestPostManualEntry_借贷不平衡时拒绝且不落库，一组固定金额）验证过，
// 从没有随机金额/随机行数/随机科目组合攻击过这条判据。
//
// 构造方式：先拼出 N 组"同金额一借一贷"的配对行（天然平衡），
// balanced=false 时再单独追加一笔不成对的借方行去打破平衡——差额
// 由随机数决定，永远非零。
func TestProperty_借贷不平衡的分录被拒绝且不落库(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")

	rapid.Check(t, func(rt *rapid.T) {
		ctx := context.Background()
		balanced := rapid.Bool().Draw(rt, "balanced")
		pairs := rapid.IntRange(1, 4).Draw(rt, "pairs")

		var lines []Line
		for i := 0; i < pairs; i++ {
			amt := rapid.Int64Range(1, 1000000).Draw(rt, fmt.Sprintf("amt%d", i))
			debitCode := rapid.SampledFrom(accountCodes).Draw(rt, fmt.Sprintf("debitCode%d", i))
			creditCode := rapid.SampledFrom(accountCodes).Draw(rt, fmt.Sprintf("creditCode%d", i))
			lines = append(lines,
				Line{AccountCode: debitCode, Debit: centsToDecimal(amt)},
				Line{AccountCode: creditCode, Credit: centsToDecimal(amt)},
			)
		}
		if !balanced {
			delta := rapid.Int64Range(1, 100000).Draw(rt, "delta")
			extraCode := rapid.SampledFrom(accountCodes).Draw(rt, "extraCode")
			lines = append(lines, Line{AccountCode: extraCode, Debit: centsToDecimal(delta)})
		}

		var countBefore int
		if err := db.QueryRow(`SELECT count(*) FROM erp_finance.finance_journal_entries`).Scan(&countBefore); err != nil {
			rt.Fatalf("查过账前 finance_journal_entries 行数失败：%v", err)
		}

		entry, err := r.PostManualEntry(ctx, PostManualEntryInput{
			IdempotencyKey:        uniqueID("prop-balance"),
			LegalEntityID:         "default",
			Lines:                 lines,
			AllowedLegalEntityIDs: defaultLegalEntities,
		})

		if balanced {
			if err != nil {
				rt.Fatalf("这组行借贷合计相等，PostManualEntry 不该拒绝：%v（lines=%+v）", err, lines)
			}
			if entry == nil || entry.Status != EntryPosted {
				rt.Fatalf("期望成功过账为 POSTED，实际 %+v", entry)
			}
			var d, c float64
			for _, l := range entry.Lines {
				dv, _ := strconv.ParseFloat(zeroIfEmpty(l.Debit), 64)
				cv, _ := strconv.ParseFloat(zeroIfEmpty(l.Credit), 64)
				d += dv
				c += cv
			}
			if diff := d - c; diff > 0.005 || diff < -0.005 {
				rt.Fatalf("不变式违反：落库后借方合计 %.2f 与贷方合计 %.2f 不相等", d, c)
			}
		} else {
			if !errors.Is(err, ErrUnbalancedEntry) {
				rt.Fatalf("这组行借贷合计不相等，期望 ErrUnbalancedEntry，实际：%v（entry=%+v, lines=%+v）", err, entry, lines)
			}
			if entry != nil {
				rt.Fatalf("借贷不平衡时不该返回非空 entry：%+v", entry)
			}
			var countAfter int
			if err := db.QueryRow(`SELECT count(*) FROM erp_finance.finance_journal_entries`).Scan(&countAfter); err != nil {
				rt.Fatalf("查过账后 finance_journal_entries 行数失败：%v", err)
			}
			if countAfter != countBefore {
				rt.Fatalf("不变式违反：借贷不平衡被拒绝，但 finance_journal_entries 行数从 %d 变成了 %d——校验必须在任何落库之前完成", countBefore, countAfter)
			}
		}
	})
}

// TestProperty_PostManualEntry并发同key仅执行一次 补的是
// 04-testing-standard.md §3.2 明确要求、此前项目里从没写过的那一半幂等性
// 测试："多个并发请求带着同一个 idempotency_key 同时到达"，而不是
// "串行重放同一个 key 两次"（已有的 TestPostManualEntry_幂等 只验证了
// 串行重放）。随机出并发数与一组固定平衡分录，重复攻击
// claimIdempotency 的 `INSERT ... ON CONFLICT DO NOTHING` 判据——只要有
// 一次并发窗口没锁住，postNo 序列就会多分配出一个、`finance_journal_entries`
// 就会多出一行。
func TestProperty_PostManualEntry并发同key仅执行一次(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(50)
	r := New(db, "erp_finance_rw", "erp_finance")

	rapid.Check(t, func(rt *rapid.T) {
		ctx := context.Background()
		concurrency := rapid.IntRange(2, 20).Draw(rt, "concurrency")
		amt := rapid.Int64Range(1, 1000000).Draw(rt, "amt")
		key := uniqueID("prop-idem-key")
		lines := []Line{
			{AccountCode: "1122", Debit: centsToDecimal(amt)},
			{AccountCode: "6001", Credit: centsToDecimal(amt)},
		}

		results := make([]*Entry, concurrency)
		errs := make([]error, concurrency)
		var wg sync.WaitGroup
		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			i := i
			go func() {
				defer wg.Done()
				results[i], errs[i] = r.PostManualEntry(ctx, PostManualEntryInput{
					IdempotencyKey:        key,
					LegalEntityID:         "default",
					Lines:                 lines,
					AllowedLegalEntityIDs: defaultLegalEntities,
				})
			}()
		}
		wg.Wait()

		var firstID string
		for i, err := range errs {
			if err != nil {
				rt.Fatalf("并发 PostManualEntry 用同一个 idempotency_key，goroutine %d 不该报错：%v", i, err)
			}
			if results[i] == nil {
				rt.Fatalf("goroutine %d 返回了空 entry", i)
			}
			if firstID == "" {
				firstID = results[i].ID
			} else if results[i].ID != firstID {
				rt.Fatalf("幂等性被打破：同一个 idempotency_key 的 %d 次并发调用应返回同一个 entry id，实际出现 %q 与 %q 两个不同的值",
					concurrency, firstID, results[i].ID)
			}
		}

		var n int
		if err := db.QueryRow(`SELECT count(*) FROM erp_finance.finance_journal_entries WHERE id = $1`,
			mustParseInt64(rt, firstID)).Scan(&n); err != nil {
			rt.Fatalf("查 finance_journal_entries 失败：%v", err)
		}
		if n != 1 {
			rt.Fatalf("幂等性被打破：id=%s 期望恰好 1 行，实际 %d 行", firstID, n)
		}
	})
}

func mustParseInt64(rt *rapid.T, s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		rt.Fatalf("entry id 不是数字：%q", s)
	}
	return n
}
