// 事件驱动的自动过账：消费 sales.order.created.v1 / erp.inventory.adjusted.v1
// 生成凭证。幂等靠凭证头上 (source_component, source_doc_type, source_doc_id,
// source_revision) 这条唯一约束，不经过 command_idempotency——调用方是
// backend/internal/consumer，不是 gRPC/HTTP。
//
// 认领在 postEntryTx 里用 INSERT … ON CONFLICT DO NOTHING 做：冲突时什么都不写、
// 返回 errSourceAlreadyPosted，这里当成重复安全跳过。不能"先插、撞了唯一约束再
// 捕获错误"：PostgreSQL 里一条语句失败后整个事务作废，随后的语句（含 COMMIT）
// 全部失败；也不能"先查有没有、再插"：两个并发事务都查不到。
//
// 每个操作都有两个入口：*Tx 版本接一个已经打开的 *sql.Tx（供
// backend/internal/consumer 在 besdk.Consume 给的事务里直接调用，不能再开一个
// besdk.WithTx——那是另一个会话，不是同一个事务）；不带 Tx 后缀的方法自己开
// 事务，供测试和直接调用的场景用。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// defaultLegalEntityID：上游事件不带法人，自动凭证记在迁移建好的默认法人名下。
const defaultLegalEntityID = "default"

// SalesOrderEventInput 是消费 sales.order.created.v1 的载荷解析成的入参。
// LegalEntityID 留给测试与直接调用时显式指定；事件消费路径（backend/internal/consumer）
// 永远传空串——erp-sales 的事件没有法人字段，走默认法人。
type SalesOrderEventInput struct {
	OrderID       string
	CustomerID    string
	Amount        string // 决定凭证金额与已用额度增量，decimal-as-string
	LegalEntityID string // 空则用 defaultLegalEntityID
	EventVersion  int64
}

// postSalesOrderEntryTx 是核心逻辑：生成应收凭证（借 1122 应收账款 /
// 贷 6001 主营业务收入）+ 一条 ar_ledger 台账行 + 累加
// customer_credit_exposure；超限发 finance.credit.rejected.v1。信用额度判定发生两次：
// erp-sales 建单前用本地缓存快速预判，这里过账时用权威值再判一次，超限就通知
// erp-sales 把订单挂起。duplicate=true 表示这个源单已经处理过，安全跳过。
func postSalesOrderEntryTx(ctx context.Context, tx *sql.Tx, in SalesOrderEventInput) (duplicate bool, err error) {
	legalEntityID := in.LegalEntityID
	if legalEntityID == "" {
		legalEntityID = defaultLegalEntityID
	}

	businessDate := time.Now().UTC()
	entry, err := postEntryTx(ctx, tx, postEntryTxInput{
		LegalEntityID: legalEntityID, BusinessDate: businessDate,
		Lines: []Line{
			{AccountCode: "1122", Debit: in.Amount},
			{AccountCode: "6001", Credit: in.Amount},
		},
		Memo:            "销售订单 " + in.OrderID,
		SourceComponent: "erp-sales", SourceDocType: "order", SourceDocID: in.OrderID, SourceRevision: in.EventVersion,
	})
	if errors.Is(err, errSourceAlreadyPosted) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	// 上游事件不带付款条件：到期日就是记账当天（见即付）。
	if err := insertARLedgerEntryTx(ctx, tx, in.CustomerID, entry.ID, legalEntityID, in.Amount, businessDate); err != nil {
		return false, err
	}

	newExposure, err := applyCreditExposureDeltaTx(ctx, tx, in.CustomerID, in.Amount)
	if err != nil {
		return false, err
	}
	limit, err := getCreditLimitTx(ctx, tx, in.CustomerID)
	if err != nil {
		return false, err
	}
	// limit == "0" 视为"还没配额度"（mdm-customer 的默认值），不是
	// "额度为零、什么都不许赊"——否则每个新客户的第一张订单都会被拒。
	exceeded, err := exceedsLimit(newExposure, limit)
	if err != nil {
		return false, err
	}
	if exceeded {
		if err := publishCreditRejected(tx, in.CustomerID, in.OrderID, newExposure, limit); err != nil {
			return false, err
		}
	}
	return false, nil
}

// PostSalesOrderEntryTx 供 backend/internal/consumer 在 besdk.Consume
// 给的事务里直接调用。
func PostSalesOrderEntryTx(ctx context.Context, tx *sql.Tx, in SalesOrderEventInput, logger *slog.Logger) error {
	duplicate, err := postSalesOrderEntryTx(ctx, tx, in)
	if err != nil {
		return err
	}
	if duplicate {
		logger.Info("销售订单事件重复（同一源单已过账），跳过", "order_id", in.OrderID)
	}
	return nil
}

// PostSalesOrderEntry 自己开事务——供测试与非事件消费场景直接调用。
func (r *Repo) PostSalesOrderEntry(ctx context.Context, in SalesOrderEventInput) (duplicate bool, err error) {
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		var werr error
		duplicate, werr = postSalesOrderEntryTx(ctx, tx, in)
		return werr
	})
	return duplicate, err
}

// exceedsLimit：已用额度严格大于额度值才算超限，按分精确比较。limit 为 0 视为
// "还没配额度"，不拦。两个值都来自 NUMERIC(18,2) 列的文本形式。
func exceedsLimit(exposure, limit string) (bool, error) {
	l, err := parseCents("credit_limit", limit)
	if err != nil {
		return false, err
	}
	if l.Sign() == 0 {
		return false, nil
	}
	e, err := parseCents("exposure", exposure)
	if err != nil {
		return false, err
	}
	return e.Cmp(l) > 0, nil
}

func publishCreditRejected(tx *sql.Tx, customerID, orderID, exposure, limit string) error {
	payload, err := json.Marshal(map[string]any{
		"customer_id": customerID, "order_id": orderID, "exposure": exposure, "limit": limit,
	})
	if err != nil {
		return err
	}
	return publish(tx, besdk.Event{
		Subject: "finance.credit.rejected.v1", AggregateID: orderID, Version: 1, Payload: payload,
	})
}

// InventoryAdjustedEventInput 对应消费 erp.inventory.adjusted.v1 的载荷
// （erp-inventory 的 contracts/events/inventory.events.json）。
type InventoryAdjustedEventInput struct {
	ProductID     string
	WarehouseID   string
	QtyDelta      string // 带符号
	MovementID    string
	Reason        string // RECEIVE/ISSUE/ADJUST_GAIN/ADJUST_LOSS
	LegalEntityID string
	EventVersion  int64
}

// postInventoryAdjustedEntryTx：生成存货科目凭证。金额是占位换算
// （amount = abs(qty_delta)，1 单位 = 1 元），不是真实成本方法：erp-inventory 的事件
// 只带数量不带金额，本组件也不依赖 mdm-product 去查 standard_cost。真实成本方法
// （标准成本 / 移动加权 / FIFO）该放在哪个组件还没定，见 docs/design.md 的未决问题。
func postInventoryAdjustedEntryTx(ctx context.Context, tx *sql.Tx, in InventoryAdjustedEventInput) (duplicate bool, err error) {
	legalEntityID := in.LegalEntityID
	if legalEntityID == "" {
		legalEntityID = defaultLegalEntityID
	}
	amount, err := placeholderAmount(in.QtyDelta)
	if err != nil {
		return false, err
	}
	if amount == "0.00" {
		return false, nil // 数量变动为 0，没有需要记的凭证
	}
	debitCode, creditCode := accountsForInventoryReason(in.Reason)

	_, err = postEntryTx(ctx, tx, postEntryTxInput{
		LegalEntityID: legalEntityID, BusinessDate: time.Now().UTC(),
		Lines: []Line{
			{AccountCode: debitCode, Debit: amount},
			{AccountCode: creditCode, Credit: amount},
		},
		Memo: fmt.Sprintf("库存调整 movement_id=%s product_id=%s reason=%s",
			in.MovementID, in.ProductID, in.Reason),
		SourceComponent: "erp-inventory", SourceDocType: "movement",
		SourceDocID: in.MovementID, SourceRevision: in.EventVersion,
	})
	if errors.Is(err, errSourceAlreadyPosted) {
		return true, nil
	}
	return false, err
}

// PostInventoryAdjustedEntryTx 供 backend/internal/consumer 直接调用。
func PostInventoryAdjustedEntryTx(ctx context.Context, tx *sql.Tx, in InventoryAdjustedEventInput, logger *slog.Logger) error {
	duplicate, err := postInventoryAdjustedEntryTx(ctx, tx, in)
	if err != nil {
		return err
	}
	if duplicate {
		logger.Info("库存调整事件重复（同一流水已过账），跳过", "movement_id", in.MovementID)
	}
	return nil
}

// PostInventoryAdjustedEntry 自己开事务——供测试与非事件消费场景直接调用。
func (r *Repo) PostInventoryAdjustedEntry(ctx context.Context, in InventoryAdjustedEventInput) (duplicate bool, err error) {
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		var werr error
		duplicate, werr = postInventoryAdjustedEntryTx(ctx, tx, in)
		return werr
	})
	return duplicate, err
}

// accountsForInventoryReason 是简化的会计处理：入库借存货、贷应付（一律当赊购，
// 没有采购组件告诉我们实际付款方式）；出库贷存货、借主营业务成本；盘盈盘亏借贷
// 互换但都对存货与成本，不单独开"盘盈盘亏"科目——迁移只预置 5 个最小科目，
// 完整科目表是本地化的事。
func accountsForInventoryReason(reason string) (debitCode, creditCode string) {
	switch reason {
	case "RECEIVE":
		return "1405", "2202"
	case "ISSUE":
		return "6401", "1405"
	case "ADJUST_GAIN":
		return "1405", "6401"
	case "ADJUST_LOSS":
		return "6401", "1405"
	default:
		return "1405", "6401"
	}
}

func placeholderAmount(qtyDelta string) (string, error) {
	c, err := absRoundCents("qty_delta", qtyDelta)
	if err != nil {
		return "", err
	}
	return formatCents(c), nil
}
