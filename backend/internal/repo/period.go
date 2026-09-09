// 会计期间：三态管理 + 跨组件期间锁的两层防护（设计计划 §2.2、§3.1）。
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const (
	PeriodOpen   = "OPEN"
	PeriodClosed = "CLOSED"
	PeriodLocked = "LOCKED"
)

// CheckPeriodOpen 是**咨询性**的，不是权威判定（设计计划 §3.1）：上游
// 改历史单据前先问一句，用来给用户一个及时的、体面的报错。真正的权威
// 判定发生在过账事务内部（见 entry.go 的 postEntryTx），check 与
// write 之间的竞态是接受的（设计计划 §9 第 3 条）。
func (r *Repo) CheckPeriodOpen(ctx context.Context, period, legalEntityID string) (string, error) {
	var status string
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM accounting_periods WHERE period = $1 AND legal_entity_id = $2`,
			period, legalEntityID).Scan(&status)
		if err == sql.ErrNoRows {
			status = ""
			return nil
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("查 accounting_periods: %w", err)
	}
	return status, nil
}

// PeriodOpInput 是 ClosePeriod/ReopenPeriod/LockPeriod 共用的入参。
type PeriodOpInput struct {
	IdempotencyKey string
	Period         string
	LegalEntityID  string
	// AllowedLegalEntityIDs 是调用者当前的 legal_entity_access 授权列表
	// （阶段三 Task 6）——service 层从 besdk.ScopeOf(ctx) 取 sub 查出来
	// 再传进来。请求体点名的 LegalEntityID 不在这份列表里就是
	// ErrForbidden：关/开/锁期间是强操作，写路径必须和读路径一样受
	// legal_entity 维数据权限约束，不能只保护读接口。
	AllowedLegalEntityIDs []string
}

// transitionPeriod 是三个期间操作共用的状态机：from 是允许的起始状态
// 集合，to 是目标状态。已经处于目标状态视为幂等重复（如实返回，不报
// 错）；处于既非起始也非目标的状态视为非法流转（报错，不是静默忽略——
// 期间状态变更是显式的会计动作，调用方传错状态应该被看见，同 CancelIssue
// 的判据不同：那是"状态内省"接口，这里是"确定要发生的一次转换"）。
func transitionPeriod(ctx context.Context, r *Repo, in PeriodOpInput, command string, from, to string) (string, error) {
	if in.IdempotencyKey == "" {
		return "", fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if !containsString(in.AllowedLegalEntityIDs, in.LegalEntityID) {
		return "", ErrForbidden
	}
	var status string
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		claimed, err := claimIdempotency(ctx, tx, in.IdempotencyKey, command)
		if err != nil {
			return err
		}
		if !claimed {
			status, err = lookupIdempotencyResult(ctx, tx, in.IdempotencyKey)
			return err
		}

		var current string
		err = tx.QueryRowContext(ctx, `
			SELECT status FROM accounting_periods
			WHERE period = $1 AND legal_entity_id = $2 FOR UPDATE`,
			in.Period, in.LegalEntityID).Scan(&current)
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: 期间 %s/%s 不存在", ErrNotFound, in.Period, in.LegalEntityID)
		}
		if err != nil {
			return fmt.Errorf("查 accounting_periods: %w", err)
		}

		switch {
		case current == to:
			status = to // 已经是目标状态：幂等重复，直接返回，不报错
		case current == from:
			if _, err := tx.ExecContext(ctx, `
				UPDATE accounting_periods SET status = $1, version = version + 1, updated_at = now()
				WHERE period = $2 AND legal_entity_id = $3`,
				to, in.Period, in.LegalEntityID); err != nil {
				return fmt.Errorf("更新 accounting_periods: %w", err)
			}
			status = to
		default:
			return fmt.Errorf("%w: 期间当前状态是 %s，不能直接变成 %s（必须先经过 %s）",
				ErrInvalidArgument, current, to, from)
		}
		return finalizeIdempotency(ctx, tx, in.IdempotencyKey, status)
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// ClosePeriod：OPEN → CLOSED（可逆的日常操作，设计计划 §2.2）。
func (r *Repo) ClosePeriod(ctx context.Context, in PeriodOpInput) (string, error) {
	return transitionPeriod(ctx, r, in, "ClosePeriod", PeriodOpen, PeriodClosed)
}

// ReopenPeriod：CLOSED → OPEN。
func (r *Repo) ReopenPeriod(ctx context.Context, in PeriodOpInput) (string, error) {
	return transitionPeriod(ctx, r, in, "ReopenPeriod", PeriodClosed, PeriodOpen)
}

// LockPeriod：CLOSED → LOCKED（终态，不可逆——必须先 ClosePeriod 才能
// LockPeriod，不能从 OPEN 直接跳过去）。
func (r *Repo) LockPeriod(ctx context.Context, in PeriodOpInput) (string, error) {
	return transitionPeriod(ctx, r, in, "LockPeriod", PeriodClosed, PeriodLocked)
}

// periodRow 是过账时锁住的那一行期间数据。
type periodRow struct {
	Period      string
	Status      string
	LastPostSeq int64
}

// lockOpenPeriodForDate 是过账事务内部的权威判定（§3.1 第二层，不可
// 绕过）：按业务日期找到覆盖它的期间；如果那个期间不是 OPEN，按"迟到的
// 凭证记进下一个开放期间"（§3.1，会计上叫"以后期间调整"）顺延到下一个
// OPEN 期间，而不是拒绝。⚠️ FOR UPDATE：这把锁同时用来序列化 post_no
// 的分配（设计计划 §9 第 7 条），不是只为了判状态。
func lockOpenPeriodForDate(ctx context.Context, tx *sql.Tx, legalEntityID string, businessDate time.Time) (*periodRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT period, status, last_post_seq
		FROM accounting_periods
		WHERE legal_entity_id = $1 AND end_date >= $2
		ORDER BY start_date ASC
		FOR UPDATE`,
		legalEntityID, businessDate)
	if err != nil {
		return nil, fmt.Errorf("查 accounting_periods: %w", err)
	}
	defer rows.Close()

	var candidates []periodRow
	for rows.Next() {
		var p periodRow
		if err := rows.Scan(&p.Period, &p.Status, &p.LastPostSeq); err != nil {
			return nil, err
		}
		candidates = append(candidates, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range candidates {
		if p.Status == PeriodOpen {
			return &p, nil
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: 业务日期 %s（法人 %s）之后没有任何已建的会计期间",
			ErrNotFound, businessDate.Format("2006-01-02"), legalEntityID)
	}
	return nil, fmt.Errorf("%w: 业务日期 %s 及其后的会计期间都不是开放状态",
		ErrPeriodNotOpen, businessDate.Format("2006-01-02"))
}

// nextPostNo 在同一把已经锁住的期间行上分配下一个过账号——同一期间内
// 连续无缺口（只在事务提交时才真正占号，回滚不留缺口，设计计划 §9
// 第 7 条）。
func nextPostNo(ctx context.Context, tx *sql.Tx, legalEntityID string, p *periodRow) (string, error) {
	next := p.LastPostSeq + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounting_periods SET last_post_seq = $1, updated_at = now()
		WHERE period = $2 AND legal_entity_id = $3`,
		next, p.Period, legalEntityID); err != nil {
		return "", fmt.Errorf("更新 last_post_seq: %w", err)
	}
	return fmt.Sprintf("P-%s-%06d", p.Period, next), nil
}
