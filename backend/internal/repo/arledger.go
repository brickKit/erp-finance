// 应收台账，与总账分家（设计计划 §2.4）：可变，核销状态挂在这里，不用
// 去改可能已经归档的总账分区。阶段二只有读接口 + 事件驱动的写入口，
// 核销（reconcile）留给阶段三收款流程。
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

type ARLedgerEntry struct {
	ID           string
	CustomerID   string
	CustomerName string
	EntryID      string
	Amount       string
	Reconciled   string
	Outstanding  string
	DueDate      string
	CreatedAt    time.Time
}

// insertARLedgerEntryTx 写一行应收。dueDate 是到期日（账龄从它算起）。
func insertARLedgerEntryTx(ctx context.Context, tx *sql.Tx, customerID, entryID, legalEntityID, amount string, dueDate time.Time) error {
	entryIDInt, err := parseID("entry_id", entryID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ar_ledger (customer_id, entry_id, legal_entity_id, amount, due_date)
		VALUES ($1, $2, $3, $4, $5)`,
		customerID, entryIDInt, legalEntityID, amount, dueDate.UTC().Format("2006-01-02")); err != nil {
		return fmt.Errorf("写 ar_ledger: %w", err)
	}
	return nil
}

type ListARLedgerInput struct {
	Cursor        string
	PageSize      int
	CustomerID    string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	// AllowedLegalEntityIDs 见 period.go 的 PeriodOpInput 同名字段注释。
	AllowedLegalEntityIDs []string
}

type ListARLedgerResult struct {
	Entries    []*ARLedgerEntry
	NextCursor string
}

func (r *Repo) ListARLedger(ctx context.Context, in ListARLedgerInput) (*ListARLedgerResult, error) {
	q := besdk.ListWindow(besdk.Query{
		From: in.CreatedAfter, To: in.CreatedBefore, Cursor: in.Cursor, Limit: in.PageSize,
	})
	ck, err := parseCursor(q.Cursor)
	if err != nil {
		return nil, err
	}

	var out ListARLedgerResult
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		// 客户名取自客户摘要副本（LEFT JOIN：没收到过这个客户的事件就是空串）；
		// 未核销余额在 SQL 里按 NUMERIC 算，不经过浮点。
		query := `SELECT a.id, a.customer_id, COALESCE(s.name, ''), a.entry_id, a.amount, a.reconciled_amount,
				(a.amount - a.reconciled_amount)::numeric(18,2)::text, to_char(a.due_date, 'YYYY-MM-DD'), a.created_at
			FROM ar_ledger a LEFT JOIN customer_credit_snapshots s ON s.customer_id = a.customer_id
			WHERE a.created_at >= $1 AND a.created_at <= $2`
		args := []any{q.From, q.To}
		args = append(args, in.AllowedLegalEntityIDs)
		query += fmt.Sprintf(" AND a.legal_entity_id = ANY($%d::text[])", len(args))
		if in.CustomerID != "" {
			args = append(args, in.CustomerID)
			query += fmt.Sprintf(" AND a.customer_id = $%d", len(args))
		}
		if ck != nil {
			args = append(args, ck.CreatedAt, ck.ID)
			query += fmt.Sprintf(" AND (a.created_at, a.id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, q.Limit+1)
		query += fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d", len(args))

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("查 ar_ledger: %w", err)
		}
		defer rows.Close()

		var entries []*ARLedgerEntry
		for rows.Next() {
			var e ARLedgerEntry
			var rawID, rawEntryID int64
			if err := rows.Scan(&rawID, &e.CustomerID, &e.CustomerName, &rawEntryID, &e.Amount, &e.Reconciled,
				&e.Outstanding, &e.DueDate, &e.CreatedAt); err != nil {
				return err
			}
			e.ID = strconv.FormatInt(rawID, 10)
			e.EntryID = strconv.FormatInt(rawEntryID, 10)
			entries = append(entries, &e)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if len(entries) > q.Limit {
			last := entries[q.Limit-1]
			lastRawID, err := strconv.ParseInt(last.ID, 10, 64)
			if err != nil {
				return err
			}
			out.NextCursor = encodeCursor(cursorKey{CreatedAt: last.CreatedAt, ID: lastRawID})
			entries = entries[:q.Limit]
		}
		out.Entries = entries
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ARSummaryInput 是应收统计的入参。AsOf 是统计当天（账龄从到期日算到这一天），
// service 层传今天（UTC），测试传固定日期。
type ARSummaryInput struct {
	CustomerID string
	AsOf       time.Time
	// AllowedLegalEntityIDs 见 period.go 的 PeriodOpInput 同名字段注释。
	AllowedLegalEntityIDs []string
}

// ARAging 是未核销余额的账龄分桶，按天数 = 统计当天 − 到期日。
type ARAging struct {
	D0To30  string
	D31To60 string
	D61To90 string
	D90Plus string
}

type ARSummary struct {
	AsOf            string // 统计当天，YYYY-MM-DD
	TotalReceivable string
	TotalReconciled string
	Outstanding     string
	Aging           ARAging
}

// SummarizeARLedger 汇总调用者有权看到的应收：合计、已核销、未核销，以及未核销余额
// 的账龄分桶（≤30 天含未到期、31–60、61–90、>90）。全部在 SQL 里按 NUMERIC 求和，
// round(…, 2) 保证结果恰好两位小数（没有数据时是 "0.00"）。
//
// 不套列表的默认 90 天窗口：这是对全部未结清应收的统计，超过 90 天的正是最要看的那一桶。
// 数据范围与列表相同：legal_entity_id 必须在授权列表里，空列表什么都匹配不到。
func (r *Repo) SummarizeARLedger(ctx context.Context, in ARSummaryInput) (*ARSummary, error) {
	asOf := in.AsOf.UTC().Format("2006-01-02")
	query := `
		SELECT round(COALESCE(sum(amount), 0), 2)::text,
		       round(COALESCE(sum(reconciled_amount), 0), 2)::text,
		       round(COALESCE(sum(amount - reconciled_amount), 0), 2)::text,
		       round(COALESCE(sum(amount - reconciled_amount) FILTER (WHERE $1::date - due_date <= 30), 0), 2)::text,
		       round(COALESCE(sum(amount - reconciled_amount) FILTER (WHERE $1::date - due_date BETWEEN 31 AND 60), 0), 2)::text,
		       round(COALESCE(sum(amount - reconciled_amount) FILTER (WHERE $1::date - due_date BETWEEN 61 AND 90), 0), 2)::text,
		       round(COALESCE(sum(amount - reconciled_amount) FILTER (WHERE $1::date - due_date > 90), 0), 2)::text
		FROM ar_ledger WHERE legal_entity_id = ANY($2::text[])`
	args := []any{asOf, in.AllowedLegalEntityIDs}
	if in.CustomerID != "" {
		args = append(args, in.CustomerID)
		query += fmt.Sprintf(" AND customer_id = $%d", len(args))
	}
	out := ARSummary{AsOf: asOf}
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, query, args...).Scan(
			&out.TotalReceivable, &out.TotalReconciled, &out.Outstanding,
			&out.Aging.D0To30, &out.Aging.D31To60, &out.Aging.D61To90, &out.Aging.D90Plus)
	})
	if err != nil {
		return nil, fmt.Errorf("汇总 ar_ledger: %w", err)
	}
	return &out, nil
}
