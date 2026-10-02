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
