// 凭证的读接口：GetEntry、ListEntries。
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

func getEntryTx(ctx context.Context, tx *sql.Tx, id string) (*Entry, error) {
	entryID, err := parseID("id", id)
	if err != nil {
		return nil, err
	}
	var e Entry
	var postedAt sql.NullTime
	row := tx.QueryRowContext(ctx, `
		SELECT id, entry_no, post_no, period, legal_entity_id, status,
			source_component, source_doc_type, source_doc_id, source_revision,
			memo, version, created_at, posted_at
		FROM finance_journal_entries WHERE id = $1`, entryID)
	var rawID int64
	if err := row.Scan(&rawID, &e.EntryNo, &e.PostNo, &e.Period, &e.LegalEntityID, &e.Status,
		&e.SourceComponent, &e.SourceDocType, &e.SourceDocID, &e.SourceRevision,
		&e.Memo, &e.Version, &e.CreatedAt, &postedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: entry id=%s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("查 finance_journal_entries: %w", err)
	}
	e.ID = strconv.FormatInt(rawID, 10)
	if postedAt.Valid {
		e.PostedAt = &postedAt.Time
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT a.code, l.debit, l.credit, l.memo
		FROM finance_journal_entry_lines l JOIN accounts a ON a.id = l.account_id
		WHERE l.entry_id = $1 ORDER BY l.id`, entryID)
	if err != nil {
		return nil, fmt.Errorf("查 finance_journal_entry_lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.AccountCode, &l.Debit, &l.Credit, &l.Memo); err != nil {
			return nil, err
		}
		e.Lines = append(e.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &e, nil
}

// GetEntry：allowedLegalEntityIDs 是调用者当前的 legal_entity_access
// 授权列表——查到的凭证不在这份列表里就是 ErrForbidden，不是
// ErrNotFound（同 erp-inventory 的 ErrForbidden 判据：凭证真实存在，
// 调用者只是看不见）。
func (r *Repo) GetEntry(ctx context.Context, id string, allowedLegalEntityIDs []string) (*Entry, error) {
	var e *Entry
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		var err error
		e, err = getEntryTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !containsString(allowedLegalEntityIDs, e.LegalEntityID) {
		return nil, ErrForbidden
	}
	return e, nil
}

// ListInput 对应 ListEntriesRequest。没有 offset 字段：列表一律游标分页。
type ListInput struct {
	Cursor        string
	PageSize      int
	Period        string
	StatusFilter  string
	SourceDocID   string
	SourceDocType string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	// AllowedLegalEntityIDs 见 period.go 的 PeriodOpInput 同名字段注释——必须下推进
	// SQL 的 WHERE 子句：查出来再在 Go 里过滤，一页就不满、游标也会跳过行。
	AllowedLegalEntityIDs []string
}

type ListResult struct {
	Entries    []*Entry
	NextCursor string
}

func buildListQuery(in ListInput) besdk.Query {
	return besdk.ListWindow(besdk.Query{
		From: in.CreatedAfter, To: in.CreatedBefore, Cursor: in.Cursor, Limit: in.PageSize,
	})
}

func (r *Repo) ListEntries(ctx context.Context, in ListInput) (*ListResult, error) {
	q := buildListQuery(in)
	ck, err := parseCursor(q.Cursor)
	if err != nil {
		return nil, err
	}

	var out ListResult
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query := `SELECT id FROM finance_journal_entries WHERE created_at >= $1 AND created_at <= $2`
		args := []any{q.From, q.To}
		// ⚠️ legal_entity_access 过滤永远加——空/nil 列表让 `= ANY(...)`
		// 天然匹配不到任何行，这是"没有分配=谁都看不见"的正确 fail-closed
		// 结果（同 erp-inventory 的 ListMovements 既有判据）。
		args = append(args, in.AllowedLegalEntityIDs)
		query += fmt.Sprintf(" AND legal_entity_id = ANY($%d::text[])", len(args))
		if in.Period != "" {
			args = append(args, in.Period)
			query += fmt.Sprintf(" AND period = $%d", len(args))
		}
		if in.StatusFilter != "" {
			args = append(args, in.StatusFilter)
			query += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if in.SourceDocID != "" {
			args = append(args, in.SourceDocID)
			query += fmt.Sprintf(" AND source_doc_id = $%d", len(args))
		}
		if in.SourceDocType != "" {
			args = append(args, in.SourceDocType)
			query += fmt.Sprintf(" AND source_doc_type = $%d", len(args))
		}
		if ck != nil {
			args = append(args, ck.CreatedAt, ck.ID)
			query += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, q.Limit+1)
		query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("查 finance_journal_entries: %w", err)
		}
		var ids []int64
		for rows.Next() {
			var rawID int64
			if err := rows.Scan(&rawID); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, rawID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// N+1：每个 id 再查一次头 + 行。列表的页大小上限与默认时间窗口
		// （besdk.ListWindow）把 N 卡在合理范围内；真影响性能时改成一次批量查询。
		entries := make([]*Entry, 0, len(ids))
		for _, rawID := range ids {
			e, err := getEntryTx(ctx, tx, strconv.FormatInt(rawID, 10))
			if err != nil {
				return err
			}
			entries = append(entries, e)
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
