// Package consumer 消费三个不同来源的事件——本阶段消费面最重的组件
// （设计计划 §4）：
//   - sales.order.created.v1（erp-sales）  → 生成应收凭证 + 累加已用额度
//   - erp.inventory.adjusted.v1（erp-inventory） → 生成存货科目凭证
//   - mdm.customer.created.v1/.updated.v1（mdm-customer） → 维护信用额度摘要副本
//
// 三个 subject 各自跑在自己的 goroutine 里（besdk.Consume 是阻塞到 ctx
// 取消才返回的循环），互不影响。
package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/erp-finance/backend/internal/repo"
)

func Start(ctx context.Context, db *sql.DB, role, schema string, nc *nats.Conn, logger *slog.Logger) error {
	subjects := []struct {
		subject string
		handle  func(context.Context, *sql.Tx, besdk.Event) error
	}{
		{"sales.order.created.v1", salesOrderHandler(logger)},
		{"erp.inventory.adjusted.v1", inventoryAdjustedHandler(logger)},
		{"mdm.customer.created.v1", customerSnapshotHandler()},
		{"mdm.customer.updated.v1", customerSnapshotHandler()},
	}

	errCh := make(chan error, len(subjects))
	for _, s := range subjects {
		s := s
		go func() {
			errCh <- besdk.Consume(ctx, nc, db, role, schema, s.subject, s.handle)
		}()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err // ⚠️ 返回 error，不许 log.Fatal（§13.3 铁律七）
	}
}

// salesOrderPayload 是本组件先按设计计划 §4 的描述假定的形状——
// erp-sales 还没建（Task 15-18），写它的事件契约时要回头对一遍
// （见 repo.SalesOrderEventInput 的同一条注释）。
type salesOrderPayload struct {
	OrderID       string `json:"order_id"`
	CustomerID    string `json:"customer_id"`
	Amount        string `json:"amount"`
	LegalEntityID string `json:"legal_entity_id"`
}

func salesOrderHandler(logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p salesOrderPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		// ⚠️ PostSalesOrderEntryTx 而不是 repo.Repo 的 PostSalesOrderEntry：
		// 这个 handler 已经在 besdk.Consume 给的事务里，不能再开一个
		// besdk.WithTx（那是另一个独立会话，不是同一个事务）。见
		// repo/autoentry.go 顶部注释。
		return repo.PostSalesOrderEntryTx(ctx, tx, repo.SalesOrderEventInput{
			OrderID: p.OrderID, CustomerID: p.CustomerID, Amount: p.Amount,
			LegalEntityID: p.LegalEntityID, EventVersion: ev.Version,
		}, logger)
	}
}

// inventoryAdjustedPayload 字段直接照抄 erp-inventory 已经真实存在的
// 契约（contracts/events/inventory.events.json，Task 8）。
type inventoryAdjustedPayload struct {
	ProductID   string `json:"product_id"`
	WarehouseID string `json:"warehouse_id"`
	QtyDelta    string `json:"qty_delta"`
	MovementID  string `json:"movement_id"`
	Reason      string `json:"reason"`
}

func inventoryAdjustedHandler(logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p inventoryAdjustedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		return repo.PostInventoryAdjustedEntryTx(ctx, tx, repo.InventoryAdjustedEventInput{
			ProductID: p.ProductID, WarehouseID: p.WarehouseID, QtyDelta: p.QtyDelta,
			MovementID: p.MovementID, Reason: p.Reason, EventVersion: ev.Version,
		}, logger)
	}
}

type customerPayload struct {
	ID          string `json:"id"`
	CreditLimit string `json:"credit_limit"`
}

func customerSnapshotHandler() func(context.Context, *sql.Tx, besdk.Event) error {
	return func(_ context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p customerPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		return repo.UpsertCustomerCreditSnapshotTx(tx, p.ID, p.CreditLimit, ev.Version)
	}
}
