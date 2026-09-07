// Package repo 是 erp-finance 的数据访问层：会计年度/期间、科目表、
// 凭证头/明细、应收/应付台账、客户信用额度（已用值 + 摘要副本）。
//
// ⚠️ 幂等过账是这一层最重的责任：唯一约束 + claim-first 声明两层缺一
// 不可（设计计划 §2.3），过账时在同一事务里锁期间行做权威判定
// （§3.1）。
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// ── 哨兵错误。grpc/http 两层通过 service.ToStatus 统一映射（同 erp-inventory）──

var ErrInvalidArgument = errors.New("参数不合法")
var ErrNotFound = errors.New("not found")

// ErrPeriodNotOpen：过账时期间不是 OPEN（权威判定，§3.1 第二层防护）。
var ErrPeriodNotOpen = errors.New("会计期间不是开放状态")

// ErrUnbalancedEntry：分录借贷不平——复式记账的基本要求。
var ErrUnbalancedEntry = errors.New("凭证借贷不平衡")

// ErrEntryNotPosted：对一张还没过账的凭证做只有 POSTED 才能做的操作
// （如 ReverseEntry）。
var ErrEntryNotPosted = errors.New("凭证尚未过账")

type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

// ── 幂等：claim-first（同 erp-inventory 的判据，Reserve 的重量级写操作
// 都要这个）。ClosePeriod/ReopenPeriod/LockPeriod/PostManualEntry/
// ReverseEntry 五个真正的写命令用它；事件驱动的自动过账
// （PostSalesOrderEntry/PostInventoryAdjustedEntry）不用——它们的幂等
// 键是 (source_component, source_doc_type, source_doc_id, source_revision)
// 这条唯一约束本身，不经过 command_idempotency（设计计划 §2.3）。
func claimIdempotency(ctx context.Context, tx *sql.Tx, key, command string) (claimed bool, err error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, '')
		 ON CONFLICT (idempotency_key) DO NOTHING`,
		key, command)
	if err != nil {
		return false, fmt.Errorf("声明 command_idempotency: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func finalizeIdempotency(ctx context.Context, tx *sql.Tx, key, resultID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE command_idempotency SET result_id = $1 WHERE idempotency_key = $2`, resultID, key)
	if err != nil {
		return fmt.Errorf("落地 command_idempotency 结果: %w", err)
	}
	return nil
}

func lookupIdempotencyResult(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var resultID string
	if err := tx.QueryRowContext(ctx,
		`SELECT result_id FROM command_idempotency WHERE idempotency_key = $1`, key).Scan(&resultID); err != nil {
		return "", fmt.Errorf("查 command_idempotency: %w", err)
	}
	return resultID, nil
}

func accountIDByCode(ctx context.Context, tx *sql.Tx, code string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE code = $1`, code).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("%w: 科目 %q 不存在", ErrInvalidArgument, code)
	}
	if err != nil {
		return 0, fmt.Errorf("查 accounts: %w", err)
	}
	return id, nil
}

func parseID(field, s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s 不合法：%q", ErrInvalidArgument, field, s)
	}
	return id, nil
}
