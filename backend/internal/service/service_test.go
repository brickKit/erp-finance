package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brickKit/erp-finance/backend/internal/repo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func testDB(t *testing.T) *sql.DB {
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
	return db
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	db := testDB(t)
	r := repo.New(db, "erp_finance_rw", "erp_finance")
	return New(r, slog.Default())
}

var seq int64

func uniqueID(prefix string) string {
	n := atomic.AddInt64(&seq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}

func TestClosePeriod_idempotencyKey为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.ClosePeriod(ctx, repo.PeriodOpInput{Period: "2026-09", LegalEntityID: "default"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("idempotency_key 为空应该拒绝，实际：%v", err)
	}
}

func TestClosePeriod_period为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.ClosePeriod(ctx, repo.PeriodOpInput{IdempotencyKey: uniqueID("k"), LegalEntityID: "default"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("period 为空应该拒绝，实际：%v", err)
	}
}

func TestPostManualEntry_lines为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: uniqueID("k"), LegalEntityID: "default",
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("lines 为空应该拒绝，实际：%v", err)
	}
}

func TestPostManualEntry_accountId为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: uniqueID("k"), LegalEntityID: "default",
		Lines: []repo.Line{{AccountCode: "", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("account_id 为空应该拒绝，实际：%v", err)
	}
}

func TestPostManualEntry_legalEntityId为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: uniqueID("k"),
		Lines:          []repo.Line{{AccountCode: "1122", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("legal_entity_id 为空应该拒绝，实际：%v", err)
	}
}

func TestReverseEntry_entryId为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.ReverseEntry(ctx, repo.ReverseEntryInput{IdempotencyKey: uniqueID("k")})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("entry_id 为空应该拒绝，实际：%v", err)
	}
}

func TestGetEntry_id为空时拒绝(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetEntry(ctx, "")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("id 为空应该拒绝，实际：%v", err)
	}
}

func TestPostManualEntry_成功过账通过service层(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	entry, err := svc.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: uniqueID("svc-manual"), LegalEntityID: "default",
		Lines: []repo.Line{{AccountCode: "1122", Debit: "30"}, {AccountCode: "6001", Credit: "30"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != repo.EntryPosted {
		t.Fatalf("期望 POSTED，实际 %q", entry.Status)
	}
}
