package ledger

import (
	"context"
	"fmt"
	"strings"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/audit"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/money"
	"github.com/brickKit/be-sdk-go/v3/search"
	apd "github.com/cockroachdb/apd/v3"
)

// Draft is a voucher about to be written: a manual draft, or a system voucher posted at once from a fact.
type Draft struct {
	ID                string
	LegalEntityID     string
	BusinessDate      string
	PostingDate       string
	Description       string
	Source            Source
	State             string // draft (manual) or posted (Issue numbers it in the same transaction)
	MakerID           *string
	ReversesVoucherID *string
	Lines             []Line
}

// Write checks a voucher's lines (VC01: balanced in the base currency and in each transaction currency, on postable
// active accounts) and writes it with its lines; a line of amount zero is left out.
func Write(ctx context.Context, tx db.Tx, d Draft) (string, error) {
	var lines []Line
	for _, l := range d.Lines {
		if isZero(l.Amount) && isZero(l.BaseAmount) {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) < 2 {
		return "", core.ErrUnbalanced.New("a voucher has a debit and a credit")
	}
	if err := balanced(lines); err != nil {
		return "", err
	}
	for _, l := range lines {
		if err := Postable(ctx, tx, d.LegalEntityID, l.AccountCode); err != nil {
			return "", err
		}
	}
	id := d.ID
	if id == "" {
		id = ids.New().String()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO voucher (id, legal_entity_id, period, business_date, posting_date, description,
		source_kind, source_id, state, maker_id, reverses_voucher_id) VALUES ($1, $2, $3, $4::date, $5::date, $6, $7, $8,
		$9, $10, $11)`, id, d.LegalEntityID, d.PostingDate[:7], d.BusinessDate, d.PostingDate, d.Description, d.Source.Kind,
		d.Source.ID, d.State, d.MakerID, d.ReversesVoucherID); err != nil {
		return "", err
	}
	return id, writeLines(ctx, tx, id, lines)
}

// Rewrite replaces a draft's lines whole and its date and description (VC01).
func Rewrite(ctx context.Context, tx db.Tx, id string, d Draft) error {
	var lines []Line
	for _, l := range d.Lines {
		if !isZero(l.Amount) {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		return core.ErrUnbalanced.New("a voucher has a debit and a credit")
	}
	if err := balanced(lines); err != nil {
		return err
	}
	for _, l := range lines {
		if err := Postable(ctx, tx, d.LegalEntityID, l.AccountCode); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM voucher_line WHERE voucher_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE voucher SET period = $2, business_date = $3::date, posting_date = $3::date,
		description = $4 WHERE id = $1`, id, d.BusinessDate[:7], d.BusinessDate, d.Description); err != nil {
		return err
	}
	return writeLines(ctx, tx, id, lines)
}

func writeLines(ctx context.Context, tx db.Tx, id string, lines []Line) error {
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO voucher_line (id, voucher_id, line_no, account_code, side, currency, amount,
			base_amount, partner_id, department_id) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10)`,
			ids.New().String(), id, i+1, l.AccountCode, l.Side, l.Currency, l.Amount, l.BaseAmount, l.PartnerID,
			l.DepartmentID); err != nil {
			return err
		}
	}
	return nil
}

func isZero(s string) bool {
	d, _, err := apd.NewFromString(s)
	return err != nil || d.IsZero()
}

// balanced is VC01: debits equal credits in the base currency, and in each transaction currency.
func balanced(lines []Line) error {
	sums := map[string]*apd.Decimal{}
	add := func(key, v, side string) error {
		d, err := money.Parse(v)
		if err != nil {
			return core.Invalid("lines", "an amount is a decimal string")
		}
		if sums[key] == nil {
			sums[key] = new(apd.Decimal)
		}
		if side == "credit" {
			d.Neg(d)
		}
		_, err = apd.BaseContext.WithPrecision(80).Add(sums[key], sums[key], d)
		return err
	}
	for _, l := range lines {
		if l.Side != "debit" && l.Side != "credit" {
			return core.Invalid("lines", "a side is debit or credit")
		}
		if err := add("base", l.BaseAmount, l.Side); err != nil {
			return err
		}
		if err := add("tx:"+l.Currency, l.Amount, l.Side); err != nil {
			return err
		}
	}
	for k, s := range sums {
		if !s.IsZero() {
			return core.ErrUnbalanced.New(fmt.Sprintf("the %s debits and credits differ by %s", strings.TrimPrefix(k, "tx:"), s.Text('f')))
		}
	}
	return nil
}

// Issue posts a written voucher (VC03): the next number of the legal entity's continuous series for the period, taken
// in this transaction (a rollback gives it back), the posted fact, and the audit naming the number (after a full
// recovery the tool raises the series past it, be-protocol P14.5).
func Issue(m *core.Base, ctx context.Context, tx db.Tx, id, leCode, actor, op string, approver *string) (Voucher, error) {
	v, err := Get(ctx, tx, id, true)
	if err != nil {
		return v, err
	}
	series := db.Series{LegalEntity: v.LegalEntityID, Code: "JV", Period: strings.ReplaceAll(v.Period, "-", "")}
	n, err := db.NextNumber(ctx, tx, series)
	if err != nil {
		return v, err
	}
	number := fmt.Sprintf("JV-%s-%s-%06d", leCode, series.Period, n)
	if _, err := tx.Exec(ctx, `UPDATE voucher SET state = 'posted', number = $2, approver_id = coalesce($3, approver_id),
		reason = NULL, search = $4, search_version = $5, version = version + 1, updated_at = now() WHERE id = $1`,
		id, number, approver, search.Text(number), search.Version); err != nil {
		return v, err
	}
	if v, err = Get(ctx, tx, id, false); err != nil {
		return v, err
	}
	if err := core.Fact(m, ctx, tx, core.VoucherPosted, v.ID, v.LegalEntityID, map[string]any{
		"fact_id": v.ID, "legal_entity_id": v.LegalEntityID, "number": number, "period": v.Period,
		"business_date": v.BusinessDate, "posting_date": v.PostingDate,
		"source": map[string]any{"kind": v.Source.Kind, "id": deref(v.Source.ID)}, "reverses_voucher_id": v.ReversesVoucherID,
		"lines": v.Lines, "recorded_at": canon.Time(time.Now())}); err != nil {
		return v, err
	}
	return v, core.ChangeWith(m, ctx, tx, actor, op, core.Vouchers, v.ID, v,
		map[string]any{"document_number": audit.DocumentNumber(series, n)})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Convert gives resolved lines their currency and base amounts (docs/design.md, AR04): the base amount is the amount
// times the fact's rate snapshot, rounded to the base currency's places line by line; a rounding difference goes to the
// largest line (the first of equals), so the base balances too. A nil rate: the transaction currency is the base.
func Convert(lines []Line, currency string, rate *string, baseScale int32) []Line {
	out := make([]Line, len(lines))
	ctx := apd.BaseContext.WithPrecision(80)
	var diff apd.Decimal
	largest := -1
	var top apd.Decimal
	for i, l := range lines {
		out[i] = l
		out[i].Currency = currency
		out[i].BaseAmount = l.Amount
		if rate != nil {
			out[i].BaseAmount = core.Times(l.Amount, *rate, baseScale)
		}
		b, _, _ := apd.NewFromString(out[i].BaseAmount)
		if l.Side == "debit" {
			_, _ = ctx.Add(&diff, &diff, b)
		} else {
			_, _ = ctx.Sub(&diff, &diff, b)
		}
		if largest < 0 || b.Cmp(&top) > 0 {
			largest, top = i, *b
		}
	}
	if rate == nil || diff.IsZero() || largest < 0 {
		return out // in the base currency an unbalanced fact stays unbalanced: Write refuses it
	}
	if out[largest].Side == "credit" {
		diff.Neg(&diff)
	}
	var adjusted apd.Decimal
	_, _ = ctx.Sub(&adjusted, &top, &diff)
	adjusted.Reduce(&adjusted)
	out[largest].BaseAmount = adjusted.Text('f')
	return out
}
