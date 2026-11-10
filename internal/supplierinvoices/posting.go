package supplierinvoices

import (
	"context"
	"fmt"
	"sort"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
)

// post is SI03 in the transaction (the cell decided Move to posted): a matched order invoice is matched again on the
// basis taken just before (a changed basis sends it back to mismatch, nothing posted); then the voucher, the payable,
// the invoiced quantities and the price confirmations, all in this transaction.
func post(m *core.Base, ctx context.Context, tx db.Tx, v Invoice, ev evidence, d core.Decision, actor string) error {
	orderLines := v.orderLines()
	sort.Strings(orderLines)
	for _, id := range orderLines { // one poster of an order line at a time: the invoiced quantities stay exact
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('finance.invoiced:' || $1))`, id); err != nil {
			return err
		}
	}
	if v.State == Matched {
		invoiced, err := invoicedOf(ctx, tx, orderLines)
		if err != nil {
			return err
		}
		if results, ok := evaluate(v, ev.basis, invoiced, m.ToleranceOf(v.LegalEntityID)); !ok {
			return record(ctx, tx, v, results, false)
		}
	}
	vid, err := voucher(m, ctx, tx, v, ev, d, actor)
	if err != nil {
		return err
	}
	item, err := openitems.Form(m, ctx, tx, openitems.New{LegalEntityID: v.LegalEntityID, Kind: "payable",
		PartnerID: v.SupplierID, Currency: v.Currency, Amount: v.Gross, DueDate: v.DueDate, Origin: "supplier_invoice",
		Source: openitems.Source{Kind: "supplier_invoice", ID: &v.ID, Number: &v.Number}, VoucherID: vid,
		DepartmentID: v.DepartmentID, DepartmentPath: v.DepartmentPath})
	if err != nil {
		return err
	}
	for _, l := range v.Lines {
		if l.OrderLineID == nil {
			continue
		}
		if err := invoiced(m, ctx, tx, v, l, ev.basis[*l.OrderLineID]); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE supplier_invoice SET state = 'posted', open_item_id = $2, voucher_id = $3,
		version = version + 1, updated_at = now() WHERE id = $1 AND state = $4`, v.ID, item.ID, vid, v.State)
	if err == nil && tag.RowsAffected() != 1 {
		err = fmt.Errorf("supplierinvoices: %s is no longer %s", v.ID, v.State)
	}
	return err
}

// voucher posts the rule supplier_invoice: the order lines' net on purchase, each expense line's on its account, the
// tax on input_tax, the gross on payable; the posting date postponed past a closed month (PD01).
func voucher(m *core.Base, ctx context.Context, tx db.Tx, v Invoice, ev evidence, d core.Decision, actor string) (string, error) {
	amounts := map[string]string{"input_tax": v.Tax, "payable": v.Gross}
	var orderNet []string
	var expenses []ledger.Line
	for _, l := range v.Lines {
		if l.OrderLineID != nil {
			orderNet = append(orderNet, l.Net)
		} else {
			expenses = append(expenses, ledger.Line{AccountCode: *l.ExpenseAccountCode, Side: "debit", Amount: l.Net})
		}
	}
	if len(orderNet) > 0 {
		amounts["purchase"] = core.Sum(orderNet...)
	}
	lines, err := ledger.Resolve(ctx, tx, v.LegalEntityID, "supplier_invoice", amounts)
	if err != nil {
		return "", err
	}
	lines = append(lines, expenses...)
	for i := range lines {
		lines[i].PartnerID, lines[i].DepartmentID = &v.SupplierID, v.DepartmentID
	}
	date, err := periods.PostingDate(m, ctx, tx, v.LegalEntityID, v.InvoiceDate)
	if err != nil {
		return "", err
	}
	id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: v.LegalEntityID, BusinessDate: v.InvoiceDate,
		PostingDate: date, Description: "供应商票据 " + v.Number + "（" + v.SupplierNumber + "）",
		Source: ledger.Source{Kind: "supplier_invoice", ID: &v.ID}, State: "posted", MakerID: &v.MakerID,
		Lines: ledger.Convert(lines, v.Currency, nil, ev.scale)})
	if err != nil {
		return "", err
	}
	_, err = ledger.Issue(m, ctx, tx, id, ev.le.Code, actor, d.Op, &actor)
	return id, err
}

// invoiced adds an order line's invoice line to what is invoiced; a goods line takes its base quantity from the
// receipt movements in their order (each up to its net received less what is invoiced of it) and publishes its price
// confirmation over them (protocol 05; the price per base unit, net of tax). A service line has no movement.
func invoiced(m *core.Base, ctx context.Context, tx db.Tx, v Invoice, l Line, b *purchasev3.BasisLine) error {
	if b == nil { // an approved difference against a line erp/purchase does not know: nothing to take from
		return nil
	}
	base := toBase(b, l.Quantity)
	if _, err := tx.Exec(ctx, `INSERT INTO invoiced_line (order_line_id, quantity, base_quantity) VALUES ($1, $2, $3)
		ON CONFLICT (order_line_id) DO UPDATE SET quantity = invoiced_line.quantity + excluded.quantity,
		base_quantity = invoiced_line.base_quantity + excluded.base_quantity`, *l.OrderLineID, l.Quantity, base); err != nil {
		return err
	}
	if b.GetKind() != "goods" {
		return nil
	}
	rest := base
	var movements []map[string]string
	for _, mv := range b.GetMovements() {
		if core.Cmp(rest, "0") <= 0 {
			break
		}
		var taken string
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT trim_scale(base_quantity)::text FROM invoiced_movement
			WHERE movement_id = $1), '0')`, mv.GetMovementId()).Scan(&taken); err != nil {
			return err
		}
		avail := core.Minus(core.Minus(mv.GetReceivedBaseQuantity(), mv.GetReturnedBaseQuantity()), taken)
		take := rest
		if core.Cmp(avail, take) < 0 {
			take = avail
		}
		if core.Cmp(take, "0") <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invoiced_movement (movement_id, base_quantity) VALUES ($1, $2)
			ON CONFLICT (movement_id) DO UPDATE SET base_quantity = invoiced_movement.base_quantity + excluded.base_quantity`,
			mv.GetMovementId(), take); err != nil {
			return err
		}
		movements = append(movements, map[string]string{"movement_id": mv.GetMovementId(),
			"receipt_line_id": mv.GetReceiptLineId(), "base_quantity": core.Sum(take)})
		rest = core.Minus(rest, take)
	}
	if len(movements) == 0 {
		return nil
	}
	return core.Fact(m, ctx, tx, core.PriceConfirmed, l.ID, v.LegalEntityID, map[string]any{
		"fact_id": l.ID, "invoice_id": v.ID, "legal_entity_id": v.LegalEntityID, "supplier_id": v.SupplierID,
		"order_id": b.GetOrderId(), "order_line_id": *l.OrderLineID, "movements": movements,
		"unit_price": core.Per(core.Times(l.UnitPrice, b.GetOrderedQuantity(), 12), b.GetOrderedBaseQuantity(), core.PriceScale),
		"currency":   v.Currency, "price_basis": "net_of_tax", "recorded_at": canon.Time(time.Now())})
}
