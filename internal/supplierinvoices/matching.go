package supplierinvoices

import (
	"context"
	"encoding/json"
	"fmt"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
)

// The matching (SI02): each order line's quantity, in the order line's unit, against what is invoiceable now; its
// price against the order's; within the legal entity's tolerances.

// approvedOrder are the order states an invoice may match.
var approvedOrder = map[string]bool{"approved": true, "completed": true, "closed": true}

// invoicedOf is what posted invoices took of the order lines, in their units.
func invoicedOf(ctx context.Context, tx db.Tx, lineIDs []string) (map[string]string, error) {
	out := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT order_line_id::text, trim_scale(quantity)::text FROM invoiced_line
		WHERE order_line_id = ANY($1::uuid[])`, lineIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, q string
		if err := rows.Scan(&id, &q); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}

// toOrderUnit converts a base quantity into the order line's unit.
func toOrderUnit(b *purchasev3.BasisLine, base string) string {
	return core.Per(core.Times(base, b.GetOrderedQuantity(), 12), b.GetOrderedBaseQuantity(), core.PriceScale)
}

// toBase converts a quantity in the order line's unit into the base unit.
func toBase(b *purchasev3.BasisLine, qty string) string {
	return core.Per(core.Times(qty, b.GetOrderedBaseQuantity(), 12), b.GetOrderedQuantity(), core.PriceScale)
}

// evaluate matches each order line of v; ok when every line is.
func evaluate(v Invoice, basis map[string]*purchasev3.BasisLine, invoiced map[string]string, tol core.Tolerance) ([]*Result, bool) {
	out := make([]*Result, len(v.Lines))
	all := true
	for i, l := range v.Lines {
		if l.OrderLineID == nil {
			continue
		}
		r := &Result{Result: "ok", InvoiceableQuantity: "0", OrderUnitPrice: "0", Problems: []string{}}
		out[i] = r
		b, ok := basis[*l.OrderLineID]
		if !ok {
			r.Result, r.Problems = "difference", []string{"erp/purchase knows no order line " + *l.OrderLineID}
			all = false
			continue
		}
		r.OrderUnitPrice = core.Sum(b.GetUnitPrice())
		if b.GetLegalEntityId() != v.LegalEntityID {
			r.Problems = append(r.Problems, "the order is another legal entity's")
		}
		if b.GetSupplierId() != v.SupplierID {
			r.Problems = append(r.Problems, "the order is another supplier's")
		}
		if b.GetCurrency() != v.Currency {
			r.Problems = append(r.Problems, "the order is in "+b.GetCurrency())
		}
		if !approvedOrder[b.GetOrderState()] {
			r.Problems = append(r.Problems, "the order is "+b.GetOrderState())
		}
		done := b.GetAcceptedQuantity()
		if b.GetKind() == "goods" {
			done = toOrderUnit(b, b.GetReceivedBaseQuantity())
		}
		invoiceable := core.Minus(done, orZero(invoiced[*l.OrderLineID]))
		if core.Cmp(invoiceable, "0") < 0 {
			invoiceable = "0"
		}
		r.InvoiceableQuantity = core.Sum(invoiceable)
		limit := core.Times(invoiceable, core.Sum("1", core.Per(tol.QuantityPercent, "100", 12)), core.PriceScale)
		if core.Cmp(l.Quantity, limit) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("quantity %s exceeds the invoiceable %s (tolerance %s %%)",
				l.Quantity, r.InvoiceableQuantity, tol.QuantityPercent))
		}
		gap := core.Minus(l.UnitPrice, b.GetUnitPrice())
		if core.Cmp(gap, "0") < 0 {
			gap = core.Minus("0", gap)
		}
		if core.Cmp(gap, core.Times(b.GetUnitPrice(), core.Per(tol.PricePercent, "100", 12), core.PriceScale)) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("price %s differs from the order's %s (tolerance %s %%)",
				l.UnitPrice, r.OrderUnitPrice, tol.PricePercent))
		}
		if len(r.Problems) > 0 {
			r.Result = "difference"
			all = false
		}
	}
	return out, all
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// record writes each line's matching and the invoice's state after it (matched or mismatch); the one writer of a
// matching's outcome.
func record(ctx context.Context, tx db.Tx, v Invoice, results []*Result, ok bool) error {
	for i, l := range v.Lines {
		b, err := json.Marshal(results[i])
		if err != nil {
			return err
		}
		if results[i] == nil {
			b = nil
		}
		if _, err := tx.Exec(ctx, `UPDATE supplier_invoice_line SET match = $2 WHERE id = $1`, l.ID, b); err != nil {
			return err
		}
	}
	state := Matched
	if !ok {
		state = Mismatch
	}
	_, err := tx.Exec(ctx, `UPDATE supplier_invoice SET state = $2, version = version + 1, updated_at = now() WHERE id = $1`,
		v.ID, state)
	return err
}
