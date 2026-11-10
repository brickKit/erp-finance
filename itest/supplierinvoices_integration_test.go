//go:build integration

package itest

import (
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supplierInvoices: supplier invoices matched against erp/purchase's basis (docs/design.md, the supplier invoice
// table; SI01-SI05; D50): registered, matched within Shanghai's tolerance (quantity 5 %, price 0), a difference
// approved by another than the maker, posted into a payable, a voucher and the price confirmations.
func (fx *fixture) supplierInvoices() {
	t := fx.t
	today := fx.today()
	// order O: a goods line of 10 BX (120 EA, 12 EA a box) at 100, 96 EA (8 BX) received in two movements; a service
	// line of 5 HR at 200, 3 accepted; another supplier's line
	order, goods, service, foreign := ids.New().String(), ids.New().String(), ids.New().String(), ids.New().String()
	m1, m2, r1, r2 := ids.New().String(), ids.New().String(), ids.New().String(), ids.New().String()
	goodsBasis := func(receivedBase string, movements ...*purchasev3.ReceiptMovement) *purchasev3.BasisLine {
		return &purchasev3.BasisLine{OrderId: order, OrderLineId: goods, OrderRevision: 1, LegalEntityId: fx.le,
			SupplierId: fx.supplier, Kind: "goods", ProductId: ids.New().String(), Unit: "BX", OrderedQuantity: "10",
			OrderedBaseQuantity: "120", ReceivedBaseQuantity: receivedBase, AcceptedQuantity: "0", AcceptedAmount: "0",
			UnitPrice: "100", Currency: "CNY", OrderState: "approved", Movements: movements}
	}
	fx.purchase.set(goodsBasis("96", &purchasev3.ReceiptMovement{MovementId: m1, ReceiptLineId: r1, ReceivedBaseQuantity: "60",
		ReturnedBaseQuantity: "0"}, &purchasev3.ReceiptMovement{MovementId: m2, ReceiptLineId: r2, ReceivedBaseQuantity: "36",
		ReturnedBaseQuantity: "0"}))
	fx.purchase.set(&purchasev3.BasisLine{OrderId: order, OrderLineId: service, OrderRevision: 1, LegalEntityId: fx.le,
		SupplierId: fx.supplier, Kind: "service", Unit: "HR", OrderedQuantity: "5", OrderedBaseQuantity: "5",
		ReceivedBaseQuantity: "0", AcceptedQuantity: "3", AcceptedAmount: "600", UnitPrice: "200", Currency: "CNY",
		OrderState: "approved"})
	fx.purchase.set(&purchasev3.BasisLine{OrderId: ids.New().String(), OrderLineId: foreign, LegalEntityId: fx.le,
		SupplierId: fx.blocked, Kind: "service", Unit: "HR", OrderedQuantity: "1", OrderedBaseQuantity: "1",
		AcceptedQuantity: "1", UnitPrice: "1", Currency: "CNY", OrderState: "approved"})
	orderLine := func(id, qty, price, tax string) map[string]any {
		return map[string]any{"order_line_id": id, "quantity": qty, "unit_price": price, "tax": tax}
	}
	expense := func(account, qty, price, tax string) map[string]any {
		return map[string]any{"order_line_id": nil, "expense_account_code": account, "description": "房租", "quantity": qty,
			"unit_price": price, "tax": tax}
	}

	// SI01: a suspended supplier, a foreign currency, order and expense lines together are refused
	for _, c := range []struct {
		body   map[string]any
		code   int
		reason string
	}{
		{fx.with(fx.siBody("S-0", today, orderLine(goods, "1", "100", "13")), "supplier_id", fx.blocked), 409, "PARTNER_NOT_USABLE"},
		{fx.with(fx.siBody("S-0", today, orderLine(goods, "1", "100", "13")), "currency", "USD"), 409, "CURRENCY_NOT_USABLE"},
		{fx.siBody("S-0", today, orderLine(goods, "1", "100", "13"), expense("6602", "1", "1", "0")), 400, "REQUEST_INVALID"},
	} {
		code, out := fx.alice.do("POST", "/api/finance/supplier-invoices", c.body)
		assert.Equal(t, c.code, code, out)
		assert.Equal(t, c.reason, out["reason"], c.body)
	}

	// SI01, SI02: 10 BX at 105 and 3 HR at 200: the goods line exceeds the 8.4 invoiceable and the price; a mismatch
	inv := fx.si(fx.alice, fx.siBody("S-1", today, orderLine(goods, "10", "105", "136.5"), orderLine(service, "3", "200", "36")))
	assert.Equal(t, "draft", inv["state"])
	assert.True(t, strings.HasPrefix(inv["number"].(string), "SI-SH-"+today[:4]+"-"), inv["number"])
	assert.Equal(t, "1650", inv["net"])
	assert.Equal(t, "1822.5", inv["gross"])
	id := inv["id"].(string)
	code, out := fx.alice.do("POST", "/api/finance/supplier-invoices", fx.siBody("S-1", today, orderLine(service, "1", "200", "0")))
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "CODE_TAKEN", out["reason"], "the supplier's number again")
	fx.siRefused(fx.alice, id, "post", nil) // an order invoice posts matched only
	inv = fx.siAction(fx.alice, id, "match", nil)
	assert.Equal(t, "mismatch", inv["state"])
	assert.Equal(t, "difference", at(inv, "lines", 0, "match", "result"))
	assert.Equal(t, "8", at(inv, "lines", 0, "match", "invoiceable_quantity"))
	assert.Equal(t, "100", at(inv, "lines", 0, "match", "order_unit_price"))
	assert.Len(t, at(inv, "lines", 0, "match", "problems"), 2, inv)
	assert.Equal(t, "ok", at(inv, "lines", 1, "match", "result"))
	// SI04: not approved by its maker
	code, out = fx.alice.do("POST", "/api/finance/supplier-invoices/"+id+"/approve-difference", map[string]any{"reason": "可以"})
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "APPROVER_IS_SUBMITTER", out["reason"])

	// SI01: changed back to draft (8.4 BX is within 5 % of 8, at the order's price); SI02: matched
	inv = fx.siUpdate(id, fx.with(fx.siBody("S-1", today, orderLine(goods, "8.4", "100", "109.2"), orderLine(service, "3", "200", "36")),
		"version", inv["version"]))
	assert.Equal(t, "draft", inv["state"])
	assert.Nil(t, at(inv, "lines", 0, "match"))
	inv = fx.siAction(fx.alice, id, "match", nil)
	assert.Equal(t, "matched", inv["state"], inv)

	// SI03: the basis changed before posting (a return: 84 EA, 7 BX): posting finds the difference, back to mismatch
	fx.purchase.set(goodsBasis("84", &purchasev3.ReceiptMovement{MovementId: m1, ReceiptLineId: r1, ReceivedBaseQuantity: "60",
		ReturnedBaseQuantity: "12"}, &purchasev3.ReceiptMovement{MovementId: m2, ReceiptLineId: r2, ReceivedBaseQuantity: "36",
		ReturnedBaseQuantity: "0"}))
	inv = fx.siAction(fx.alice, id, "post", nil)
	assert.Equal(t, "mismatch", inv["state"])
	fx.purchase.set(goodsBasis("96", &purchasev3.ReceiptMovement{MovementId: m1, ReceiptLineId: r1, ReceivedBaseQuantity: "60",
		ReturnedBaseQuantity: "0"}, &purchasev3.ReceiptMovement{MovementId: m2, ReceiptLineId: r2, ReceivedBaseQuantity: "36",
		ReturnedBaseQuantity: "0"}))
	fx.siAction(fx.alice, id, "match", nil)

	// SI03: posted: the payable, the voucher (1401 840 + 600, 2221.01 145.2, 2202 1585.2), the goods line's price
	// confirmation over both movements (100.8 EA: 60 + 36 and 4.8 beyond them, at 100 a box = 8.333333 an EA); the
	// service line has none
	inv = fx.siAction(fx.alice, id, "post", nil)
	require.Equal(t, "posted", inv["state"], inv)
	fx.payable = inv["open_item_id"].(string)
	payable := fx.get("/api/finance/open-items/" + fx.payable)
	assert.Equal(t, "payable", payable["kind"])
	assert.Equal(t, fx.supplier, payable["partner_id"])
	assert.Equal(t, "1585.2", payable["amount"])
	assert.Equal(t, "supplier_invoice", payable["origin"])
	v := fx.voucherOf(id)
	got := map[string]string{}
	for _, l := range v["lines"].([]any) {
		got[at(l, "account_code").(string)+" "+at(l, "side").(string)] = at(l, "amount").(string)
	}
	assert.Equal(t, map[string]string{"1401 debit": "1440", "2221.01 debit": "145.2", "2202 credit": "1585.2"}, got)
	lines := inv["lines"].([]any)
	pc := fx.fact(at(lines[0], "id").(string), core.PriceConfirmed)
	assert.Equal(t, "8.333333", pc["unit_price"])
	assert.Equal(t, r1, at(pc, "movements", 0, "receipt_line_id"))
	assert.Equal(t, "60", at(pc, "movements", 0, "base_quantity"))
	assert.Equal(t, "36", at(pc, "movements", 1, "base_quantity"))
	assert.Len(t, pc["movements"], 2)
	fx.noFact(at(lines[1], "id").(string), core.PriceConfirmed)
	fx.siAction(fx.alice, id, "post", nil) // a repeat is ignored

	// SI02, SI04: the goods line again: nothing invoiceable is left (8.4 of 8 BX invoiced); approved by Bob, posted
	// with no movement to confirm
	diff := fx.si(fx.alice, fx.siBody("S-2", today, orderLine(goods, "1", "100", "13")))
	did := diff["id"].(string)
	diff = fx.siAction(fx.alice, did, "match", nil)
	assert.Equal(t, "mismatch", diff["state"])
	assert.Equal(t, "0", at(diff, "lines", 0, "match", "invoiceable_quantity"))
	code, out = fx.bob.do("POST", "/api/finance/supplier-invoices/"+did+"/approve-difference", map[string]any{})
	assert.Equal(t, 400, code, out, "a reason is required")
	diff = fx.siAction(fx.bob, did, "approve-difference", map[string]any{"reason": "供应商多送的一箱，留下"})
	assert.NotNil(t, diff["difference_approved_by"])
	diff = fx.siAction(fx.alice, did, "post", nil)
	assert.Equal(t, "posted", diff["state"])
	fx.noFact(at(diff["lines"].([]any)[0], "id").(string), core.PriceConfirmed)

	// SI02: another supplier's order line is a difference
	other := fx.si(fx.alice, fx.siBody("S-3", today, orderLine(foreign, "1", "1", "0")))
	other = fx.siAction(fx.alice, other["id"].(string), "match", nil)
	assert.Equal(t, "mismatch", other["state"])
	// SI01: an approved difference does not survive a change
	other = fx.siAction(fx.bob, other["id"].(string), "approve-difference", map[string]any{"reason": "先批"})
	other = fx.siUpdate(other["id"].(string), fx.with(fx.siBody("S-3", today, orderLine(foreign, "1", "1", "0")), "version", other["version"]))
	assert.Equal(t, "draft", other["state"])
	assert.Nil(t, other["difference_approved_by"])

	// SI04, SI03: an expense invoice posts approved by another; its expense account takes the net
	exp := fx.si(fx.alice, fx.siBody("S-4", today, expense("6602", "1", "500", "30")))
	eid := exp["id"].(string)
	fx.siRefused(fx.alice, eid, "post", nil) // not approved
	fx.siRefused(fx.alice, eid, "match", nil)
	code, out = fx.alice.do("POST", "/api/finance/supplier-invoices/"+eid+"/approve-difference", map[string]any{"reason": "房租"})
	assert.Equal(t, 409, code, out)
	fx.siAction(fx.bob, eid, "approve-difference", map[string]any{"reason": "十一月房租"})
	exp = fx.siAction(fx.alice, eid, "post", nil)
	assert.Equal(t, "posted", exp["state"])
	got = map[string]string{}
	for _, l := range fx.voucherOf(eid)["lines"].([]any) {
		got[at(l, "account_code").(string)+" "+at(l, "side").(string)] = at(l, "amount").(string)
	}
	assert.Equal(t, map[string]string{"6602 debit": "500", "2221.01 debit": "30", "2202 credit": "530"}, got)

	// SI05: a posted invoice neither changes nor cancels; a draft cancels (again: ignored), and its number is free again
	code, out = fx.alice.do("PATCH", "/api/finance/supplier-invoices/"+eid, fx.with(fx.siBody("S-4", today, expense("6602", "1", "1", "0")),
		"version", exp["version"]))
	assert.Equal(t, 409, code, out)
	fx.siRefused(fx.alice, eid, "cancel", nil)
	c := fx.si(fx.alice, fx.siBody("S-5", today, expense("6602", "1", "1", "0")))
	assert.Equal(t, "cancelled", fx.siAction(fx.alice, c["id"].(string), "cancel", nil)["state"])
	fx.siAction(fx.alice, c["id"].(string), "cancel", nil)
	fx.si(fx.alice, fx.siBody("S-5", today, expense("6602", "1", "1", "0")))
	l := fx.get("/api/finance/supplier-invoices?state=posted&legal_entity_id=" + fx.le)
	assert.Len(t, l["supplier_invoices"], 3, l)
}

func (fx *fixture) siBody(number, date string, lines ...map[string]any) map[string]any {
	ls := make([]any, len(lines))
	for i, l := range lines {
		ls[i] = l
	}
	return map[string]any{"legal_entity_id": fx.le, "supplier_id": fx.supplier, "supplier_number": number,
		"invoice_date": date, "due_date": date, "currency": "CNY", "department_id": fx.d1, "lines": ls}
}

func (fx *fixture) si(c client, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/finance/supplier-invoices", body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) siUpdate(id string, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("PATCH", "/api/finance/supplier-invoices/"+id, body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) siAction(c client, id, action string, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/finance/supplier-invoices/"+id+"/"+action, body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) siRefused(c client, id, action string, body map[string]any) {
	fx.t.Helper()
	code, out := c.do("POST", "/api/finance/supplier-invoices/"+id+"/"+action, body)
	assert.Equal(fx.t, 409, code, out)
	assert.Equal(fx.t, "STATE_CONFLICT", out["reason"], action)
}

// noFact asserts no fact of the type with the ID was published.
func (fx *fixture) noFact(id, typ string) {
	fx.t.Helper()
	var n int
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = $1 AND "+
		"convert_from(payload, 'UTF8')::jsonb->>'fact_id' = $2", typ, id).Scan(&n))
	assert.Zero(fx.t, n, "%s of %s", typ, id)
}
