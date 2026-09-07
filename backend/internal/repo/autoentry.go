// 事件驱动的自动过账：消费 sales.order.created.v1 / erp.inventory.adjusted.v1
// 生成凭证。幂等靠 (source_component, source_doc_type, source_doc_id,
// source_revision) 这条唯一约束本身，不经过 command_idempotency
// （设计计划 §2.3）——调用方是 backend/internal/consumer，不是 gRPC/HTTP。
//
// ⚠️ 必须先查后插，不能"先插、撞了唯一约束再捕获错误当作重复"：
// PostgreSQL 里一条语句真的执行失败后，整个事务会被标记成 aborted，
// 即使 Go 这层选择吞掉这个错误，事务在数据库那侧也回不去了，随后的
// 任何语句（包括 COMMIT）都会失败（同 mdm-product archive.go 的实测
// 踩坑）。所以这里先 SELECT 判断"这个源单是不是已经处理过"，判断结果
// 一致才真的去写。
//
// ⚠️ 每个操作都有两个入口：*Tx 版本接一个已经打开的 *sql.Tx（供
// backend/internal/consumer 在 besdk.Consume 给的事务里直接调用，不能
// 再开一个 besdk.WithTx——那是两个独立会话，不是同一个事务）；不带
// Tx 后缀的方法自己开事务，供测试和任何直接调用的场景用。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

func findExistingBySource(ctx context.Context, tx *sql.Tx, component, docType, docID string, revision int64) (string, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM finance_journal_entries
		WHERE source_component = $1 AND source_doc_type = $2 AND source_doc_id = $3 AND source_revision = $4`,
		component, docType, docID, revision).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("查 finance_journal_entries: %w", err)
	}
	return strconv.FormatInt(id, 10), true, nil
}

const defaultLegalEntityID = "default" // 阶段二只有一个默认法人（设计计划 §1）

// SalesOrderEventInput 是消费 sales.order.created.v1 的载荷解析成的入参。
// LegalEntityID 留着给测试/PostSalesOrderEntry 直调时显式指定用——真实
// 事件消费路径（backend/internal/consumer）永远传空字符串，erp-sales
// 契约本身没有法人概念（阶段二只有一个默认法人），走下面的默认值分支。
type SalesOrderEventInput struct {
	OrderID       string
	CustomerID    string
	Amount        string // 决定凭证金额与已用额度增量，decimal-as-string
	LegalEntityID string // 空则用 defaultLegalEntityID
	EventVersion  int64
}

// postSalesOrderEntryTx 是核心逻辑：生成应收凭证（借 1122 应收账款 /
// 贷 6001 主营业务收入）+ 一条 ar_ledger 台账行 + 累加
// customer_credit_exposure；超限发 finance.credit.rejected.v1（设计计划
// §5：判定发生两次，这是第二次、权威的那次）。duplicate=true 表示这个
// 源单已经处理过，安全跳过。
func postSalesOrderEntryTx(ctx context.Context, tx *sql.Tx, in SalesOrderEventInput) (duplicate bool, err error) {
	legalEntityID := in.LegalEntityID
	if legalEntityID == "" {
		legalEntityID = defaultLegalEntityID
	}

	_, found, err := findExistingBySource(ctx, tx, "erp-sales", "order", in.OrderID, in.EventVersion)
	if err != nil {
		return false, err
	}
	if found {
		return true, nil
	}

	entry, err := postEntryTx(ctx, tx, postEntryTxInput{
		LegalEntityID: legalEntityID, BusinessDate: time.Now().UTC(),
		Lines: []Line{
			{AccountCode: "1122", Debit: in.Amount},
			{AccountCode: "6001", Credit: in.Amount},
		},
		Memo:            "销售订单 " + in.OrderID,
		SourceComponent: "erp-sales", SourceDocType: "order", SourceDocID: in.OrderID, SourceRevision: in.EventVersion,
	})
	if err != nil {
		return false, err
	}

	if err := insertARLedgerEntryTx(ctx, tx, in.CustomerID, entry.ID, legalEntityID, in.Amount); err != nil {
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

func exceedsLimit(exposure, limit string) (bool, error) {
	limitF, err := strconv.ParseFloat(limit, 64)
	if err != nil {
		return false, fmt.Errorf("解析 credit_limit: %w", err)
	}
	if limitF == 0 {
		return false, nil // 未配额度，不拦
	}
	exposureF, err := strconv.ParseFloat(exposure, 64)
	if err != nil {
		return false, fmt.Errorf("解析 exposure: %w", err)
	}
	return exposureF > limitF, nil
}

func publishCreditRejected(tx *sql.Tx, customerID, orderID, exposure, limit string) error {
	payload, err := json.Marshal(map[string]any{
		"customer_id": customerID, "order_id": orderID, "exposure": exposure, "limit": limit,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, "erp_finance", besdk.Event{
		Subject: "finance.credit.rejected.v1", AggregateID: orderID, Version: 1, Payload: payload,
	})
}

// InventoryAdjustedEventInput 对应消费 erp.inventory.adjusted.v1 的载荷
// ——这份契约已经真实存在（erp-inventory Task 8），字段直接照抄。
type InventoryAdjustedEventInput struct {
	ProductID     string
	WarehouseID   string
	QtyDelta      string // 带符号
	MovementID    string
	Reason        string // RECEIVE/ISSUE/ADJUST_GAIN/ADJUST_LOSS
	LegalEntityID string
	EventVersion  int64
}

// postInventoryAdjustedEntryTx：生成存货科目凭证。⚠️ 金额是占位换算
// （amount = abs(qty_delta)，1 单位 = 1 元），不是真实成本方法——
// erp-inventory 的事件只带数量不带金额，本组件也没有依赖 mdm-product
// 去查 standard_cost（设计计划 §9 第 8 条记录了这个已知缺口，留给
// 出档复盘决定真实成本方法该怎么接）。
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

	_, found, err := findExistingBySource(ctx, tx, "erp-inventory", "movement", in.MovementID, in.EventVersion)
	if err != nil {
		return false, err
	}
	if found {
		return true, nil
	}
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

// accountsForInventoryReason 是阶段二的简化会计处理：入库借存货、贷
// 应付（假设都是赊购，阶段二没有 erp-purchase 校验实际付款方式）；
// 出库贷存货、借主营业务成本；盘盈盘亏借贷互换但都对存货与成本，
// 不单独开"盘盈盘亏"科目——5 个最小科目集里没有这一项（设计计划 §9
// 第 2 条：完整科目表是本地化的事）。
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
	f, err := strconv.ParseFloat(qtyDelta, 64)
	if err != nil {
		return "", fmt.Errorf("%w: qty_delta 不是合法数字：%q", ErrInvalidArgument, qtyDelta)
	}
	if f < 0 {
		f = -f
	}
	return strconv.FormatFloat(f, 'f', 2, 64), nil
}
