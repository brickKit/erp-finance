package repo

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func postSmallEntry(t *testing.T, r *Repo) *Entry {
	t.Helper()
	e, err := r.PostManualEntry(context.Background(), PostManualEntryInput{
		IdempotencyKey: uniqueID("to-reverse"), LegalEntityID: "default", AllowedLegalEntityIDs: defaultLegalEntities,
		Lines: []Line{{AccountCode: "1405", Debit: "5.00"}, {AccountCode: "2202", Credit: "5.00"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// 一张凭证只能被红字冲销一次：换一个幂等键再冲销一次，账上就多冲了一笔。
func TestReverseEntry_同一张凭证不能冲销两次(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	ctx := context.Background()
	e := postSmallEntry(t, r)
	if _, err := r.ReverseEntry(ctx, ReverseEntryInput{IdempotencyKey: uniqueID("rev-1"), EntryID: e.ID, AllowedLegalEntityIDs: defaultLegalEntities}); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReverseEntry(ctx, ReverseEntryInput{IdempotencyKey: uniqueID("rev-2"), EntryID: e.ID, AllowedLegalEntityIDs: defaultLegalEntities})
	if !errors.Is(err, ErrEntryAlreadyReversed) {
		t.Fatalf("第二次冲销应该报 ErrEntryAlreadyReversed，实际：%v", err)
	}
}

func TestReverseEntry_并发冲销同一张凭证只成功一次(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	ctx := context.Background()
	e := postSmallEntry(t, r)

	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.ReverseEntry(ctx, ReverseEntryInput{IdempotencyKey: uniqueID("rev-c"), EntryID: e.ID, AllowedLegalEntityIDs: defaultLegalEntities})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrEntryAlreadyReversed):
		default:
			t.Errorf("并发冲销出现意外错误：%v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("并发冲销同一张凭证应该恰好成功一次，实际 %d 次", ok)
	}
}
