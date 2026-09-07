// 凭证：核心过账逻辑（postEntryTx）+ 人工凭证/红字冲销/读接口。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const (
	EntryDraft  = "DRAFT"
	EntryPosted = "POSTED"
)

// Line 是一条分录行，AccountCode 用科目编码（"1122"）不是内部 id——
// 业务代码按编码认科目，同 mdm-product 用 SKU 而不是内部 id 的判据。
type Line struct {
	AccountCode string
	Debit       string
	Credit      string
	Memo        string
}

type Entry struct {
	ID              string
	EntryNo         string
	PostNo          string
	Period          string
	LegalEntityID   string
	Status          string
	SourceComponent string
	SourceDocType   string
	SourceDocID     string
	SourceRevision  int64
	Lines           []Line
	Memo            string
	Version         int64
	CreatedAt       time.Time
	PostedAt        *time.Time
}

// postEntryTxInput 是 postEntryTx 的入参——PostManualEntry、ReverseEntry、
// PostSalesOrderEntry、PostInventoryAdjustedEntry 都通过它复用同一套
// "解析期间 → 锁期间行权威判定 → 校验借贷平衡 → 写头写行 → 分配过账号
// → 发事件"逻辑，业务代码只写一遍。
type postEntryTxInput struct {
	LegalEntityID   string
	BusinessDate    time.Time
	Lines           []Line
	Memo            string
	SourceComponent string
	SourceDocType   string
	SourceDocID     string
	SourceRevision  int64
}

func postEntryTx(ctx context.Context, tx *sql.Tx, in postEntryTxInput) (*Entry, error) {
	if len(in.Lines) == 0 {
		return nil, fmt.Errorf("%w: 分录不能没有行", ErrInvalidArgument)
	}
	if err := validateBalanced(in.Lines); err != nil {
		return nil, err
	}

	period, err := lockOpenPeriodForDate(ctx, tx, in.LegalEntityID, in.BusinessDate)
	if err != nil {
		return nil, err
	}

	var entryID int64
	var entryNo string
	if err := tx.QueryRowContext(ctx, `SELECT nextval('entry_no_seq')`).Scan(&entryNo); err != nil {
		return nil, fmt.Errorf("生成 entry_no: %w", err)
	}
	entryNoStr := "E-" + entryNo
	postNo, err := nextPostNo(ctx, tx, in.LegalEntityID, period)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO finance_journal_entries
			(entry_no, post_no, period, legal_entity_id, status,
			 source_component, source_doc_type, source_doc_id, source_revision, memo, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		entryNoStr, postNo, period.Period, in.LegalEntityID, EntryPosted,
		in.SourceComponent, in.SourceDocType, in.SourceDocID, in.SourceRevision, in.Memo, now,
	).Scan(&entryID)
	if err != nil {
		return nil, fmt.Errorf("写 finance_journal_entries: %w", err)
	}
	entryIDStr := strconv.FormatInt(entryID, 10)

	for _, line := range in.Lines {
		accountID, err := accountIDByCode(ctx, tx, line.AccountCode)
		if err != nil {
			return nil, err
		}
		debit := zeroIfEmpty(line.Debit)
		credit := zeroIfEmpty(line.Credit)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO finance_journal_entry_lines
				(entry_id, accounting_period, legal_entity_id, account_id, debit, credit, memo)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			entryID, period.Period, in.LegalEntityID, accountID, debit, credit, line.Memo,
		); err != nil {
			return nil, fmt.Errorf("写 finance_journal_entry_lines: %w", err)
		}
	}

	if err := publishVoucherPosted(tx, entryIDStr, entryNoStr, postNo, period.Period, totalAmount(in.Lines)); err != nil {
		return nil, err
	}

	return &Entry{
		ID: entryIDStr, EntryNo: entryNoStr, PostNo: postNo, Period: period.Period,
		LegalEntityID: in.LegalEntityID, Status: EntryPosted,
		SourceComponent: in.SourceComponent, SourceDocType: in.SourceDocType,
		SourceDocID: in.SourceDocID, SourceRevision: in.SourceRevision,
		Lines: in.Lines, Memo: in.Memo, Version: 1, CreatedAt: now, PostedAt: &now,
	}, nil
}

// validateBalanced 校验借贷相等——复式记账的基本要求。金额是
// decimal-as-string，用 strconv.ParseFloat 只做求和校验，不参与落库
// （落库交给 NUMERIC，同 mdm-product 的 validateStandardCost 判据）。
func validateBalanced(lines []Line) error {
	var debitSum, creditSum float64
	for _, l := range lines {
		d, err := parseAmount("debit", l.Debit)
		if err != nil {
			return err
		}
		c, err := parseAmount("credit", l.Credit)
		if err != nil {
			return err
		}
		if d > 0 && c > 0 {
			return fmt.Errorf("%w: 一行不能同时有借方和贷方", ErrInvalidArgument)
		}
		if d == 0 && c == 0 {
			return fmt.Errorf("%w: 一行必须有借方或贷方，不能都是 0", ErrInvalidArgument)
		}
		debitSum += d
		creditSum += c
	}
	// 浮点误差容忍到分（NUMERIC(18,2)）
	if diff := debitSum - creditSum; diff > 0.005 || diff < -0.005 {
		return fmt.Errorf("%w: 借方合计 %.2f，贷方合计 %.2f", ErrUnbalancedEntry, debitSum, creditSum)
	}
	return nil
}

func parseAmount(field, s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s 不是合法数字：%q", ErrInvalidArgument, field, s)
	}
	if f < 0 {
		return 0, fmt.Errorf("%w: %s 不能为负数：%q", ErrInvalidArgument, field, s)
	}
	return f, nil
}

func zeroIfEmpty(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func totalAmount(lines []Line) string {
	var sum float64
	for _, l := range lines {
		d, _ := strconv.ParseFloat(zeroIfEmpty(l.Debit), 64)
		sum += d
	}
	return strconv.FormatFloat(sum, 'f', 2, 64)
}

func publishVoucherPosted(tx *sql.Tx, entryID, entryNo, postNo, period, amount string) error {
	payload, err := json.Marshal(map[string]any{
		"entry_id": entryID, "entry_no": entryNo, "post_no": postNo, "period": period, "amount": amount,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, "erp_finance", besdk.Event{
		Subject: "finance.voucher.posted.v1", AggregateID: entryID, Version: 1, Payload: payload,
	})
}

// ── 人工凭证 / 红字冲销（RPC 入口，claim-first 幂等）──

type PostManualEntryInput struct {
	IdempotencyKey string
	LegalEntityID  string
	Lines          []Line
	Memo           string
}

// PostManualEntry：自动凭证走事件消费，这里只有人工补录（设计计划
// §3）。source_component 留空——它没有"源单"的概念。
func (r *Repo) PostManualEntry(ctx context.Context, in PostManualEntryInput) (*Entry, error) {
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.LegalEntityID == "" {
		return nil, fmt.Errorf("%w: legal_entity_id 不能为空", ErrInvalidArgument)
	}
	var entry *Entry
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		claimed, err := claimIdempotency(ctx, tx, in.IdempotencyKey, "PostManualEntry")
		if err != nil {
			return err
		}
		if !claimed {
			existingID, err := lookupIdempotencyResult(ctx, tx, in.IdempotencyKey)
			if err != nil {
				return err
			}
			entry, err = getEntryTx(ctx, tx, existingID)
			return err
		}

		entry, err = postEntryTx(ctx, tx, postEntryTxInput{
			LegalEntityID: in.LegalEntityID, BusinessDate: time.Now().UTC(),
			Lines: in.Lines, Memo: in.Memo,
		})
		if err != nil {
			return err
		}
		return finalizeIdempotency(ctx, tx, in.IdempotencyKey, entry.ID)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

type ReverseEntryInput struct {
	IdempotencyKey string
	EntryID        string
	Reason         string
}

// ReverseEntry：红字冲销，不是删除（设计计划 §2.1）——原凭证一个字不动，
// 产生一张新的、借贷方向互换的反向凭证，记进**当前**期间（不是原凭证
// 的历史期间：改历史期间的账违反"过账后永不修改"这条更根本的原则）。
func (r *Repo) ReverseEntry(ctx context.Context, in ReverseEntryInput) (*Entry, error) {
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.EntryID == "" {
		return nil, fmt.Errorf("%w: entry_id 不能为空", ErrInvalidArgument)
	}
	var entry *Entry
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		claimed, err := claimIdempotency(ctx, tx, in.IdempotencyKey, "ReverseEntry")
		if err != nil {
			return err
		}
		if !claimed {
			existingID, err := lookupIdempotencyResult(ctx, tx, in.IdempotencyKey)
			if err != nil {
				return err
			}
			entry, err = getEntryTx(ctx, tx, existingID)
			return err
		}

		original, err := getEntryTx(ctx, tx, in.EntryID)
		if err != nil {
			return err
		}
		if original.Status != EntryPosted {
			return fmt.Errorf("%w: entry_id=%s", ErrEntryNotPosted, in.EntryID)
		}

		reversedLines := make([]Line, len(original.Lines))
		for i, l := range original.Lines {
			reversedLines[i] = Line{AccountCode: l.AccountCode, Debit: l.Credit, Credit: l.Debit,
				Memo: "冲销 " + original.EntryNo}
		}
		entry, err = postEntryTx(ctx, tx, postEntryTxInput{
			LegalEntityID: original.LegalEntityID, BusinessDate: time.Now().UTC(),
			Lines: reversedLines, Memo: fmt.Sprintf("冲销凭证 %s：%s", original.EntryNo, in.Reason),
			SourceDocType: "reversal", SourceDocID: original.ID,
		})
		if err != nil {
			return err
		}
		return finalizeIdempotency(ctx, tx, in.IdempotencyKey, entry.ID)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

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

func (r *Repo) GetEntry(ctx context.Context, id string) (*Entry, error) {
	var e *Entry
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		var err error
		e, err = getEntryTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ListInput 对应 ListEntriesRequest。刻意没有 offset 字段（决策 53）。
type ListInput struct {
	Cursor        string
	PageSize      int
	Period        string
	StatusFilter  string
	CreatedAfter  time.Time
	CreatedBefore time.Time
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
	var ck *cursorKey
	if q.Cursor != "" {
		decoded, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, fmt.Errorf("非法 cursor：%w", err)
		}
		ck = &decoded
	}

	var out ListResult
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query := `SELECT id FROM finance_journal_entries WHERE created_at >= $1 AND created_at <= $2`
		args := []any{q.From, q.To}
		if in.Period != "" {
			args = append(args, in.Period)
			query += fmt.Sprintf(" AND period = $%d", len(args))
		}
		if in.StatusFilter != "" {
			args = append(args, in.StatusFilter)
			query += fmt.Sprintf(" AND status = $%d", len(args))
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

		// ⚠️ N+1：每个 id 再查一次头 + 行。List 的默认页大小与强制时间
		// 窗口（besdk.ListWindow）把 N 卡在合理范围内，阶段二先接受这个
		// 代价——真影响性能了再优化成一次批量 JOIN 查询。
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
