package settlement

import (
	"context"
	"encoding/json"

	claims "github.com/brickKit/erp-finance/v3/internal/claims"
	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/db"
)

// reverse takes a fund fact back whole (ST05): its voucher mirrored, each of its allocations reversed (the original
// kept), the formalized claims reversed (CL09), the credit balance it formed cancelled. Before the fact it reverses
// is posted, or when it cannot be taken back whole, it waits.
func reverse(m *core.Base, ctx context.Context, tx db.Tx, f core.Received, ff fundFact) (string, error) {
	if ff.ReversesFactID == nil {
		return "", core.ErrWaitForOriginal
	}
	var state, data string
	var original *string
	err := tx.QueryRow(ctx, `SELECT state, data::text, voucher_id::text FROM fact_received WHERE fact_id = $1 FOR UPDATE`,
		*ff.ReversesFactID).Scan(&state, &data, &original)
	if err != nil || state != "posted" || original == nil {
		return "", core.ErrWaitForOriginal // not received or not posted yet
	}
	var of fundFact
	if err := json.Unmarshal([]byte(data), &of); err != nil {
		return "", err
	}
	if core.Cmp(of.Amount, ff.Amount) != 0 {
		m.RT.Logger.Error("a partial reversal waits for a person", "event", "reversal_partial", "fact_id", ff.FactID,
			"reverses", of.FactID)
		return "", core.ErrWaitForOriginal
	}
	// the credit balance the receipt formed: cancelled, when nothing of it is used yet
	var credit []string
	if err := scan(ctx, tx, `SELECT id::text FROM open_item WHERE origin = 'fund' AND source_id = $1`, &credit, of.FactID); err != nil {
		return "", err
	}
	// the allocations to take back, and every open item they and the credit balance touch, locked in ID order
	type taken struct {
		id, item, amount string
		claim            *string
	}
	var back []taken
	rows, err := tx.Query(ctx, `SELECT id::text, open_item_id::text, trim_scale(amount)::text, claim_id FROM allocation
		WHERE fund_fact_id = $1 AND direction = 'settle' ORDER BY id`, of.FactID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var t taken
		if err := rows.Scan(&t.id, &t.item, &t.amount, &t.claim); err != nil {
			rows.Close()
			return "", err
		}
		back = append(back, t)
	}
	rows.Close()
	ids := append([]string{}, credit...)
	for _, t := range back {
		ids = append(ids, t.item)
	}
	locked, err := lockItems(ctx, tx, ids)
	if err != nil {
		return "", err
	}
	for _, id := range credit {
		if o := locked[id]; core.Cmp(o.Settled, "0") != 0 || core.Cmp(o.PayClaimed, "0") != 0 {
			m.RT.Logger.Error("a reversal waits: the credit balance it would take back is used", "event",
				"reversal_credit_used", "fact_id", ff.FactID, "open_item_id", id)
			return "", core.ErrWaitForOriginal
		}
	}
	vid, err := mirror(m, ctx, tx, f, ff, *original)
	if err != nil {
		return "", err
	}
	for _, t := range back {
		if t.claim != nil {
			r, err := claims.Get(ctx, tx, *t.claim)
			if err != nil {
				return "", err
			}
			if _, err := claims.ReverseFormalized(m, ctx, tx, r, t.amount, f.Source); err != nil {
				return "", err
			}
		}
		o, err := openitems.Get(ctx, tx, t.item, false)
		if err != nil {
			return "", err
		}
		id := t.id
		if err := allocate(m, ctx, tx, allocation{fund: ff.FactID, item: o, amount: t.amount, direction: "reverse",
			claim: t.claim, reverses: &id}); err != nil {
			return "", err
		}
	}
	for _, id := range credit {
		if _, err := tx.Exec(ctx, `UPDATE open_item SET state = 'cancelled', version = version + 1, updated_at = now()
			WHERE id = $1`, id); err != nil {
			return "", err
		}
	}
	return vid, nil
}

// mirror posts the original voucher's lines with the sides swapped, and marks the original reversed.
func mirror(m *core.Base, ctx context.Context, tx db.Tx, f core.Received, ff fundFact, original string) (string, error) {
	v, err := ledger.Get(ctx, tx, original, true)
	if err != nil {
		return "", err
	}
	lines := make([]ledger.Line, len(v.Lines))
	for i, l := range v.Lines {
		lines[i] = l
		lines[i].Side = map[string]string{"debit": "credit", "credit": "debit"}[l.Side]
	}
	date, err := periods.PostingDate(m, ctx, tx, f.LegalEntityID, ff.PostingDate)
	if err != nil {
		return "", err
	}
	id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: f.LegalEntityID, BusinessDate: ff.BusinessDate,
		PostingDate: date, Description: "资金冲正 " + deref(ff.Source.DocumentNumber), Source: ledger.Source{Kind: "fund_fact",
			ID: &ff.FactID}, State: "posted", ReversesVoucherID: &original, Lines: lines})
	if err != nil {
		return "", err
	}
	if _, err := ledger.Issue(m, ctx, tx, id, f.LegalEntityCode, f.Source, core.OpPostFact, nil); err != nil {
		return "", err
	}
	return id, ledger.MarkReversed(ctx, tx, original, id)
}

func scan(ctx context.Context, tx db.Tx, q string, out *[]string, args ...any) error {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		*out = append(*out, s)
	}
	return rows.Err()
}
