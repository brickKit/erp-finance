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
	ID         string
	CustomerID string
	EntryID    string
	Amount     string
	Reconciled string
	CreatedAt  time.Time
}

func insertARLedgerEntryTx(ctx context.Context, tx *sql.Tx, customerID, entryID, legalEntityID, amount string) error {
	entryIDInt, err := parseID("entry_id", entryID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ar_ledger (customer_id, entry_id, legal_entity_id, amount)
		VALUES ($1, $2, $3, $4)`,
		customerID, entryIDInt, legalEntityID, amount); err != nil {
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
}

type ListARLedgerResult struct {
	Entries    []*ARLedgerEntry
	NextCursor string
}

func (r *Repo) ListARLedger(ctx context.Context, in ListARLedgerInput) (*ListARLedgerResult, error) {
	q := besdk.ListWindow(besdk.Query{
		From: in.CreatedAfter, To: in.CreatedBefore, Cursor: in.Cursor, Limit: in.PageSize,
	})
	var ck *cursorKey
	if q.Cursor != "" {
		decoded, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, fmt.Errorf("非法 cursor：%w", err)
		}
		ck = &decoded
	}

	var out ListARLedgerResult
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query := `SELECT id, customer_id, entry_id, amount, reconciled_amount, created_at
			FROM ar_ledger WHERE created_at >= $1 AND created_at <= $2`
		args := []any{q.From, q.To}
		if in.CustomerID != "" {
			args = append(args, in.CustomerID)
			query += fmt.Sprintf(" AND customer_id = $%d", len(args))
		}
		if ck != nil {
			args = append(args, ck.CreatedAt, ck.ID)
			query += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, q.Limit+1)
		query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("查 ar_ledger: %w", err)
		}
		defer rows.Close()

		var entries []*ARLedgerEntry
		for rows.Next() {
			var e ARLedgerEntry
			var rawID, rawEntryID int64
			if err := rows.Scan(&rawID, &e.CustomerID, &rawEntryID, &e.Amount, &e.Reconciled, &e.CreatedAt); err != nil {
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
