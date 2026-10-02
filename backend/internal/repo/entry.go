// 凭证的核心过账逻辑 postEntryTx：人工凭证、红字冲销与两种自动凭证都经过它（入口在
// manual.go、autoentry.go，读接口在 entry_read.go）。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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

// errSourceAlreadyPosted：这张源单（source_component/doc_type/doc_id/revision）已经
// 过过账。只有自动凭证会遇到，调用方把它当成重复投递安全跳过。
var errSourceAlreadyPosted = errors.New("源单已过账")

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

	// 先认领、后编号：凭证头带着空 post_no 插入，源单唯一约束（只管自动凭证）冲突时
	// DO NOTHING——同一张源单已经由另一个事务过了账，这里什么都没写，安全返回重复。
	// 不能"先查有没有、再插"：两个并发事务都查不到，后到的那个插入时撞唯一约束，整个
	// 事务作废。post_no 在认领成功之后才分配，重复的那次不占号、不留缺口
	// （entry_no 允许有缺口）。
	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO finance_journal_entries
			(entry_no, period, legal_entity_id, status,
			 source_component, source_doc_type, source_doc_id, source_revision, memo, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (source_component, source_doc_type, source_doc_id, source_revision)
			WHERE source_component != '' DO NOTHING
		RETURNING id`,
		entryNoStr, period.Period, in.LegalEntityID, EntryPosted,
		in.SourceComponent, in.SourceDocType, in.SourceDocID, in.SourceRevision, in.Memo, now,
	).Scan(&entryID)
	if err == sql.ErrNoRows {
		return nil, errSourceAlreadyPosted
	}
	if err != nil {
		return nil, fmt.Errorf("写 finance_journal_entries: %w", err)
	}
	postNo, err := nextPostNo(ctx, tx, in.LegalEntityID, period)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE finance_journal_entries SET post_no = $1 WHERE id = $2`, postNo, entryID); err != nil {
		return nil, fmt.Errorf("写 post_no: %w", err)
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

// validateBalanced 校验每一行恰好一边非零、借贷合计相等——复式记账的基本要求。
// 按分精确比较（见 money.go），不容忍任何误差。
func validateBalanced(lines []Line) error {
	debitSum, creditSum := new(big.Int), new(big.Int)
	for _, l := range lines {
		d, err := parseCents("debit", l.Debit)
		if err != nil {
			return err
		}
		c, err := parseCents("credit", l.Credit)
		if err != nil {
			return err
		}
		if d.Sign() > 0 && c.Sign() > 0 {
			return fmt.Errorf("%w: 一行不能同时有借方和贷方", ErrInvalidArgument)
		}
		if d.Sign() == 0 && c.Sign() == 0 {
			return fmt.Errorf("%w: 一行必须有借方或贷方，不能都是 0", ErrInvalidArgument)
		}
		debitSum.Add(debitSum, d)
		creditSum.Add(creditSum, c)
	}
	if debitSum.Cmp(creditSum) != 0 {
		return fmt.Errorf("%w: 借方合计 %s，贷方合计 %s", ErrUnbalancedEntry, formatCents(debitSum), formatCents(creditSum))
	}
	return nil
}

func zeroIfEmpty(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// totalAmount 是凭证金额（借方合计）。只在 validateBalanced 通过之后调用，所以
// 每一行的金额都已经是合法格式。
func totalAmount(lines []Line) string {
	sum := new(big.Int)
	for _, l := range lines {
		d, _ := parseCents("debit", l.Debit)
		sum.Add(sum, d)
	}
	return formatCents(sum)
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
