package repo

import (
	"context"
	"errors"
	"testing"
)

// 非法游标是调用方传错了参数，应该落到 ErrInvalidArgument（REST 400 / gRPC
// InvalidArgument），不能当成服务端故障报 500。
func TestListEntries_非法游标是参数错误(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	_, err := r.ListEntries(context.Background(), ListInput{Cursor: "不是游标!!", AllowedLegalEntityIDs: defaultLegalEntities})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("非法 cursor 应该是 ErrInvalidArgument，实际：%v", err)
	}
}

func TestListARLedger_非法游标是参数错误(t *testing.T) {
	db := testDB(t)
	r := New(db, "erp_finance_rw", "erp_finance")
	_, err := r.ListARLedger(context.Background(), ListARLedgerInput{Cursor: "不是游标!!", AllowedLegalEntityIDs: defaultLegalEntities})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("非法 cursor 应该是 ErrInvalidArgument，实际：%v", err)
	}
}
