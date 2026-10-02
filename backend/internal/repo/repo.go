// Package repo 是 erp-finance 的数据访问层：会计年度/期间、科目表、
// 凭证头/明细、应收/应付台账、客户信用额度（已用值 + 摘要副本）。
//
// 这一层最重的两件事：过账幂等（写命令 claim-first 认领幂等键；自动凭证靠凭证头
// 上的源单唯一约束认领），以及过账时在同一个事务里锁住期间行做权威判定。
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

// orDiscard：调用方没给 logger（测试、直接调用）时什么都不记。
func orDiscard(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return logger
}

// ── 哨兵错误。grpc/http 两层通过 service.ToStatus 统一映射（同 erp-inventory）──

var ErrInvalidArgument = errors.New("参数不合法")
var ErrNotFound = errors.New("not found")

// ErrPeriodNotOpen：过账时覆盖业务日期的期间及其后的期间都不是 OPEN（过账事务内的权威判定）。
var ErrPeriodNotOpen = errors.New("会计期间不是开放状态")

// ErrUnbalancedEntry：分录借贷不平——复式记账的基本要求。
var ErrUnbalancedEntry = errors.New("凭证借贷不平衡")

// ErrEntryNotPosted：对一张还没过账的凭证做只有 POSTED 才能做的操作
// （如 ReverseEntry）。
var ErrEntryNotPosted = errors.New("凭证尚未过账")

// ErrEntryAlreadyReversed：这张凭证已经被红字冲销过，不能再冲一次。
var ErrEntryAlreadyReversed = errors.New("凭证已被冲销")

// ErrForbidden：调用者对某个具体法人没有 legal_entity_access 授权。这个法人的
// 数据是存在的，调用者只是无权看或改，与 ErrNotFound 语义不同，不能混用。
var ErrForbidden = errors.New("无权访问该法人")

// containsString 判断 s 是不是在 allowed 里——写路径校验"请求体里点名的
// legal_entity_id 我到底有没有权限"共用的小工具。
func containsString(allowed []string, s string) bool {
	for _, v := range allowed {
		if v == s {
			return true
		}
	}
	return false
}

type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

// ── 幂等：claim-first。ClosePeriod / ReopenPeriod / LockPeriod / PostManualEntry /
// ReverseEntry 五个写命令先 INSERT … ON CONFLICT DO NOTHING 认领幂等键再写，两个
// 带同一个键的并发请求只有一个真正执行。事件驱动的自动过账不经过这张表：它们的
// 幂等键是凭证头上 (source_component, source_doc_type, source_doc_id,
// source_revision) 这条唯一约束本身（见 entry.go 的 postEntryTx）。
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
