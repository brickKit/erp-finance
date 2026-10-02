package grpc

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	financev1 "github.com/brickKit/erp-finance/gen/erp/finance/v1"

	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
	"github.com/brickKit/erp-finance/v2/backend/internal/service"
)

// syncBuffer：gRPC 的 handler 跑在别的 goroutine 里，日志缓冲要并发安全。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// 面向用户的 rpc 要调用者身份才能套用 legal_entity_access，gRPC 不带用户身份。经 gRPC
// 调它们应该回 UNAUTHENTICATED（调用方的错），而不是走到 besdk.ScopeOf 里 panic、
// 被 SDK 的 recovery 变成 INTERNAL 并在服务端打一行 ERROR。用 besdk.ServeExtraPort
// 起真的 gRPC 服务（独立运行与外壳走的都是它）。repo 不给数据库：这些调用不该碰到它。
func TestUserFacingRPCs_没有用户身份回Unauthenticated且不panic(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := service.New(repo.New(nil, "erp_finance_rw", "erp_finance"), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	go func() {
		_ = besdk.ServeExtraPort(ctx, "grpc", port, func(gs *grpc.Server) {
			financev1.RegisterFinanceServiceServer(gs, New(svc))
		}, logger)
	}()

	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true))) // 服务端在另一个 goroutine 里起，等它就绪
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := financev1.NewFinanceServiceClient(conn)

	periodOp := func(key string) (string, string, string) { return key, "2026-09", "default" }
	calls := map[string]func(context.Context) error{
		"ClosePeriod": func(ctx context.Context) error {
			k, p, le := periodOp("grpc-close")
			_, err := c.ClosePeriod(ctx, &financev1.ClosePeriodRequest{IdempotencyKey: k, Period: p, LegalEntityId: le})
			return err
		},
		"ReopenPeriod": func(ctx context.Context) error {
			k, p, le := periodOp("grpc-reopen")
			_, err := c.ReopenPeriod(ctx, &financev1.ReopenPeriodRequest{IdempotencyKey: k, Period: p, LegalEntityId: le})
			return err
		},
		"LockPeriod": func(ctx context.Context) error {
			k, p, le := periodOp("grpc-lock")
			_, err := c.LockPeriod(ctx, &financev1.LockPeriodRequest{IdempotencyKey: k, Period: p, LegalEntityId: le})
			return err
		},
		"PostManualEntry": func(ctx context.Context) error {
			_, err := c.PostManualEntry(ctx, &financev1.PostManualEntryRequest{
				IdempotencyKey: "grpc-manual", LegalEntityId: "default",
				Lines: []*financev1.JournalEntryLine{{AccountId: "1405", Debit: "1.00"}, {AccountId: "2202", Credit: "1.00"}},
			})
			return err
		},
		"ReverseEntry": func(ctx context.Context) error {
			_, err := c.ReverseEntry(ctx, &financev1.ReverseEntryRequest{IdempotencyKey: "grpc-reverse", EntryId: "e-1"})
			return err
		},
		"GetEntry": func(ctx context.Context) error {
			_, err := c.GetEntry(ctx, &financev1.GetEntryRequest{Id: "e-1"})
			return err
		},
		"ListEntries": func(ctx context.Context) error {
			_, err := c.ListEntries(ctx, &financev1.ListEntriesRequest{})
			return err
		},
		"ListARLedger": func(ctx context.Context) error {
			_, err := c.ListARLedger(ctx, &financev1.ListARLedgerRequest{})
			return err
		},
	}

	for name, call := range calls {
		callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := call(callCtx)
		callCancel()
		if got := status.Code(err); got != codes.Unauthenticated {
			t.Errorf("%s 没有用户身份时应该回 Unauthenticated，实际 %v（%v）", name, got, err)
		}
	}
	if out := logs.String(); strings.Contains(out, "panic") || strings.Contains(out, "level=ERROR") {
		t.Errorf("调用方没带身份不该在服务端 panic 或记 ERROR，实际日志：%q", out)
	}
}

// 带验过签的 Claims 时 requireUser 放行：将来 SDK 在 gRPC 端口上验 JWT 后，这些 rpc 不该被它挡住。
func TestRequireUser_有Claims时放行(t *testing.T) {
	ctx := besdk.ContextWithClaims(context.Background(), besdk.Claims{Sub: "u-1"})
	if err := requireUser(ctx); err != nil {
		t.Fatalf("有 Claims 时不该拒绝，实际：%v", err)
	}
	if got := status.Code(requireUser(context.Background())); got != codes.Unauthenticated {
		t.Fatalf("没有 Claims 时应该是 Unauthenticated，实际 %v", got)
	}
}
