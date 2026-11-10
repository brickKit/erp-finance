package settlement

import (
	"context"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/ids"
)

// allocation is one allocation to write: money of a fund fact on an open item, or the reversal of an earlier one.
type allocation struct {
	fund      string
	item      openitems.OpenItem // locked by the caller
	amount    string
	direction string // settle or reverse
	claim     *string
	reverses  *string
}

// allocate is the one writer of allocations and of an open item's settled (ST04): the allocation record, settled
// moved by it, erp.open_item.settled, and for a receivable its consequence for erp/credit (cleared at a zero open
// balance, reduced when it went down, reopened when a reversal opened it again).
func allocate(m *core.Base, ctx context.Context, tx db.Tx, a allocation) error {
	id := ids.New().String()
	if _, err := tx.Exec(ctx, `INSERT INTO allocation (id, fund_fact_id, open_item_id, amount, direction, claim_id,
		reverses_allocation_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, a.fund, a.item.ID, a.amount, a.direction, a.claim,
		a.reverses); err != nil {
		return err
	}
	delta := a.amount
	if a.direction == "reverse" {
		delta = core.Minus("0", a.amount)
	}
	if _, err := tx.Exec(ctx, `UPDATE open_item SET settled = settled + $2, version = version + 1, updated_at = now()
		WHERE id = $1`, a.item.ID, delta); err != nil {
		return err
	}
	after, err := openitems.Get(ctx, tx, a.item.ID, false)
	if err != nil {
		return err
	}
	now := canon.Time(time.Now())
	if err := core.Fact(m, ctx, tx, core.OpenItemSettled, id, after.LegalEntityID, map[string]any{"fact_id": id,
		"legal_entity_id": after.LegalEntityID, "fund_fact_id": a.fund, "open_item_id": after.ID, "kind": after.Kind,
		"partner_id": after.PartnerID, "currency": after.Currency, "amount": a.amount, "direction": a.direction,
		"claim_id": a.claim, "recorded_at": now}); err != nil {
		return err
	}
	if after.Kind != "receivable" {
		return nil
	}
	typ := core.ReceivableReduced
	switch {
	case a.direction == "reverse":
		typ = core.ReceivableReopened
	case core.Cmp(after.OpenBalance, "0") == 0:
		typ = core.ReceivableCleared
	}
	var invoice *string
	if after.Source.Kind == "invoice" {
		invoice = after.Source.ID
	}
	fact := ids.New().String()
	return core.Fact(m, ctx, tx, typ, fact, after.LegalEntityID, map[string]any{"fact_id": fact,
		"legal_entity_id": after.LegalEntityID, "customer_id": after.PartnerID, "open_item_id": after.ID,
		"invoice_id": invoice, "currency": after.Currency, "amount": a.amount, "open_balance": after.OpenBalance,
		"recorded_at": now})
}

// lockItems locks open items in ID order (protocol 05's settlement: a fixed order, no deadlock) and reads them.
func lockItems(ctx context.Context, tx db.Tx, ids []string) (map[string]openitems.OpenItem, error) {
	out := map[string]openitems.OpenItem{}
	rows, err := tx.Query(ctx, `SELECT id::text FROM open_item WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	var locked []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		locked = append(locked, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range locked {
		o, err := openitems.Get(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		out[id] = o
	}
	return out, nil
}
