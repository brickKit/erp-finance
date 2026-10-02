// 人工凭证与红字冲销：gRPC / REST 的写入口，claim-first 幂等（command_idempotency）。
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// ── 人工凭证 / 红字冲销（RPC 入口，claim-first 幂等）──

type PostManualEntryInput struct {
	IdempotencyKey string
	LegalEntityID  string
	Lines          []Line
	Memo           string
	// AllowedLegalEntityIDs 见 period.go 的 PeriodOpInput 同名字段注释。
	AllowedLegalEntityIDs []string
}

// PostManualEntry：自动凭证走事件消费，这里只有人工补录。source_component
// 留空——人工凭证没有"源单"。
func (r *Repo) PostManualEntry(ctx context.Context, in PostManualEntryInput) (*Entry, error) {
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.LegalEntityID == "" {
		return nil, fmt.Errorf("%w: legal_entity_id 不能为空", ErrInvalidArgument)
	}
	if !containsString(in.AllowedLegalEntityIDs, in.LegalEntityID) {
		return nil, ErrForbidden
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
	// AllowedLegalEntityIDs 见 period.go 的 PeriodOpInput 同名字段注释。
	// ⚠️ 与 PostManualEntry 不同：这里没有调用方直接点名的 LegalEntityID
	// ——要冲销哪个法人的账，是从"被冲销的那张原凭证"上查出来的，校验
	// 时机必须在拿到 original 之后（见下）。
	AllowedLegalEntityIDs []string
}

// ReverseEntry：红字冲销，不是删除——原凭证一个字不动，
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

		original, err := getEntryForReversalTx(ctx, tx, in.EntryID)
		if err != nil {
			return err
		}
		if !containsString(in.AllowedLegalEntityIDs, original.LegalEntityID) {
			return ErrForbidden
		}
		if original.Status != EntryPosted {
			return fmt.Errorf("%w: entry_id=%s", ErrEntryNotPosted, in.EntryID)
		}
		if err := ensureNotReversedTx(ctx, tx, original.ID); err != nil {
			return err
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

// getEntryForReversalTx 锁住要被冲销的那张凭证头再读出来：同一张凭证的并发冲销在
// 这里排队，后到的那个能看见先到的已经写下的冲销凭证（ensureNotReversedTx）。
func getEntryForReversalTx(ctx context.Context, tx *sql.Tx, id string) (*Entry, error) {
	entryID, err := parseID("id", id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM finance_journal_entries WHERE id = $1 FOR UPDATE`, entryID); err != nil {
		return nil, fmt.Errorf("锁 finance_journal_entries: %w", err)
	}
	return getEntryTx(ctx, tx, id)
}

// ensureNotReversedTx：一张凭证只能红字冲销一次，再冲一次账上就多冲了一笔。
func ensureNotReversedTx(ctx context.Context, tx *sql.Tx, entryID string) error {
	var reversed bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM finance_journal_entries
		               WHERE source_doc_type = 'reversal' AND source_doc_id = $1)`, entryID).Scan(&reversed); err != nil {
		return fmt.Errorf("查冲销凭证: %w", err)
	}
	if reversed {
		return fmt.Errorf("%w: entry_id=%s", ErrEntryAlreadyReversed, entryID)
	}
	return nil
}
