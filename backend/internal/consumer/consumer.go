// Package consumer 消费三个来源的事件，把别人发生的事变成会计语言：
//   - sales.order.created.v1（erp-sales）        → 应收凭证 + 应收台账 + 累加已用额度
//   - erp.inventory.adjusted.v1（erp-inventory） → 存货科目凭证
//   - mdm.customer.created/updated.v1（mdm-customer） → 客户摘要副本（额度值、客户名）
//
// 每个 subject 跑在自己的 goroutine 里（besdk.Consume 阻塞到 ctx 取消才返回），
// 互不影响。
package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
)

func Start(ctx context.Context, db *sql.DB, role, schema string, nc *nats.Conn, logger *slog.Logger) error {
	subjects := []struct {
		subject string
		handle  func(context.Context, *sql.Tx, besdk.Event) error
	}{
		{"sales.order.created.v1", salesOrderHandler(logger)},
		{"erp.inventory.adjusted.v1", inventoryAdjustedHandler(logger)},
		{"mdm.customer.created.v1", customerSnapshotHandler(logger)},
		{"mdm.customer.updated.v1", customerSnapshotHandler(logger)},
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
		return err // 返回 error，不 log.Fatal：进外壳后一个成员退出进程，同进程的成员全部下线
	}
}

// salesOrderPayload 是 erp-sales 的 sales.order.created.v1 里本组件用到的字段
// （contracts/events/sales.events.json）。事件不带法人：凭证记在默认法人名下
// （见 repo.SalesOrderEventInput）。
type salesOrderPayload struct {
	OrderID     string `json:"order_id"`
	CustomerID  string `json:"customer_id"`
	TotalAmount string `json:"total_amount"`
}

func salesOrderHandler(logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p salesOrderPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		// 用 PostSalesOrderEntryTx 而不是 Repo.PostSalesOrderEntry：handler 已经在
		// besdk.Consume 给的事务里（event_inbox 的记录也在这个事务里），再开一个
		// besdk.WithTx 就是另一个会话，凭证与"消息已处理"不再一起提交或回滚。
		return repo.PostSalesOrderEntryTx(ctx, tx, repo.SalesOrderEventInput{
			OrderID: p.OrderID, CustomerID: p.CustomerID, Amount: p.TotalAmount,
			EventVersion: ev.Version,
		}, logger)
	}
}

// inventoryAdjustedPayload 是 erp-inventory 的 erp.inventory.adjusted.v1 里本组件
// 用到的字段（contracts/events/inventory.events.json）。
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
	Name        string `json:"name"`
	CreditLimit string `json:"credit_limit"`
}

func customerSnapshotHandler(logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(_ context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p customerPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		return repo.UpsertCustomerSnapshotTx(tx, p.ID, p.Name, p.CreditLimit, ev.Version, logger)
	}
}
