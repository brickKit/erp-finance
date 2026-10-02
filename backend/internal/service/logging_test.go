package service

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
)

// R51：映射成调用方状态（4xx）的错误不记 ERROR——ERROR 只留给要运维处理的事。
// 五个写命令的失败原来一律 logger.Error：没有授权、借贷不平、期间不开放、
// 凭证不存在都会刷出 ERROR。
func TestWriteCommands_调用方的错误不记ERROR(t *testing.T) {
	db := testDB(t)
	r := repo.New(db, "erp_finance_rw", "erp_finance")
	var logs bytes.Buffer
	svc := New(r, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	noAccess := authedCtx(t, r, uniqueID("sub-noaccess")) // 没有任何法人授权
	granted := authedCtx(t, r, uniqueID("sub-default"), "default")
	op := func() repo.PeriodOpInput {
		return repo.PeriodOpInput{IdempotencyKey: uniqueID("k"), Period: "2026-09", LegalEntityID: "default"}
	}
	calls := map[string]func() error{
		"ClosePeriod 无授权":  func() error { _, err := svc.ClosePeriod(noAccess, op()); return err },
		"ReopenPeriod 无授权": func() error { _, err := svc.ReopenPeriod(noAccess, op()); return err },
		"LockPeriod 无授权":   func() error { _, err := svc.LockPeriod(noAccess, op()); return err },
		"PostManualEntry 借贷不平": func() error {
			_, err := svc.PostManualEntry(granted, repo.PostManualEntryInput{
				IdempotencyKey: uniqueID("k"), LegalEntityID: "default",
				Lines: []repo.Line{{AccountCode: "1405", Debit: "1.00"}, {AccountCode: "2202", Credit: "0.99"}},
			})
			return err
		},
		"ReverseEntry 凭证不存在": func() error {
			_, err := svc.ReverseEntry(granted, repo.ReverseEntryInput{
				IdempotencyKey: uniqueID("k"), EntryID: "999999999999999",
			})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if err == nil {
			t.Errorf("%s 应该失败", name)
			continue
		}
		if c := status.Code(ToStatus(err)); c == codes.Internal || c == codes.Unknown {
			t.Errorf("%s 应该是调用方的错误（4xx），实际映射到 %v：%v", name, c, err)
		}
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Errorf("调用方的错误不该记 ERROR，实际日志：%q", out)
	}
}
