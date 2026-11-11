package main

import (
	"fmt"
	"math/big"
	"time"

	"github.com/brickKit/be-sdk-go/v3/seed"
)

// invoiced is how many of erp/purchase's completed orders get a supplier invoice, by legal entity.
var invoiced = map[string]int{"CN01": 40, "ES01": 8, "US01": 8}

// taxRate is the demo input tax rate by legal entity (the tax component comes later; the user fills the tax).
var taxRate = map[string]*big.Rat{"CN01": big.NewRat(13, 100), "ES01": big.NewRat(21, 100), "US01": new(big.Rat)}

func rat(s any) *big.Rat {
	r, ok := new(big.Rat).SetString(s.(string))
	if !ok {
		fail("not a decimal: %v", s)
	}
	return r
}

// money is r rounded half up to two decimals (CNY, EUR and USD all have two).
func money(r *big.Rat) string {
	cents := new(big.Rat).Mul(r, big.NewRat(100, 1))
	n := new(big.Int).Quo(new(big.Int).Add(new(big.Int).Mul(cents.Num(), big.NewInt(2)), cents.Denom()),
		new(big.Int).Mul(cents.Denom(), big.NewInt(2)))
	return new(big.Rat).SetFrac(n, big.NewInt(100)).FloatString(2)
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

func inDays(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

// invoiceLines are an order's lines received or accepted in full (goods: the received base quantity is the ordered
// one; services: the accepted quantity is the ordered one), invoiced at the order's price, times priceBy.
func (s *seeder) invoiceLines(o map[string]any, priceBy *big.Rat) []any {
	var out []any
	for _, l := range o["lines"].([]any) {
		l := l.(map[string]any)
		full := l["kind"] == "goods" && rat(l["received_base_quantity"]).Cmp(rat(l["base_quantity"])) == 0
		full = full || (l["kind"] == "service" && rat(l["accepted_quantity"]).Cmp(rat(l["quantity"])) == 0)
		if !full {
			continue
		}
		price := new(big.Rat).Mul(rat(l["unit_price"]), priceBy)
		net := new(big.Rat).Mul(rat(l["quantity"]), rat(money(price)))
		out = append(out, map[string]any{"order_line_id": l["id"], "quantity": l["quantity"], "unit_price": money(price),
			"tax": money(new(big.Rat).Mul(rat(money(net)), taxRate[s.e.code]))})
	}
	return out
}

// supplierInvoices registers invoices against erp/purchase's completed orders in the base currency, one per order, and
// moves each on by its place: a draft, a price difference approved and posted, cancelled, matched only, or matched
// and posted; then expense invoices: two approved and posted, one a draft.
func (s *seeder) supplierInvoices() int {
	purchase := client{seed.NewClient(env("ERP_PURCHASE_URL", "http://localhost:18085"), s.seed.Token)}
	n := 0
	for _, o := range purchase.pages("/api/purchase/orders?state=completed&legal_entity_id="+s.e.id, "orders") {
		if n == invoiced[s.e.code] {
			break
		}
		if o["currency"] != s.e.currency {
			continue // base currency invoices only in this version (SI01)
		}
		priceBy := big.NewRat(1, 1)
		if n%8 == 1 {
			priceBy = big.NewRat(105, 100) // a price difference: the tolerance is 0 unless MATCH_TOLERANCES says more
		}
		lines := s.invoiceLines(o, priceBy)
		if len(lines) == 0 {
			continue
		}
		body := map[string]any{"legal_entity_id": s.e.id, "supplier_id": o["supplier_id"], "supplier_number": "INV-" + o["number"].(string),
			"invoice_date": today(), "due_date": inDays(30), "currency": s.e.currency, "department_id": o["department_id"],
			"lines": lines}
		s.invoice(n, body, map[int]string{0: "draft", 1: "posted", 2: "cancelled", 3: "matched"}[n%8])
		n++
	}
	for k, want := range []string{"posted", "posted", "draft"} {
		body := map[string]any{"legal_entity_id": s.e.id, "supplier_id": s.e.suppliers[k%len(s.e.suppliers)],
			"supplier_number": fmt.Sprintf("EXP-%s-%d", s.e.code, k+1), "invoice_date": today(), "due_date": inDays(15),
			"currency": s.e.currency, "lines": []any{map[string]any{"description": prefix + "物业与水电费",
				"expense_account_code": "6602", "quantity": "1", "unit_price": fmt.Sprintf("%d.00", 800+k*350), "tax": "0"}}}
		s.invoice(100+k, body, want)
		n++
	}
	return n
}

// invoice registers invoice i as the maker and moves it on to want ("" is matched and posted).
func (s *seeder) invoice(i int, body map[string]any, want string) {
	v := s.maker.must("POST", "/api/finance/supplier-invoices", body, "Idempotency-Key", s.key("invoice", i))
	path := "/api/finance/supplier-invoices/" + v["id"].(string)
	v = s.seed.must("GET", path, nil)
	expense := body["lines"].([]any)[0].(map[string]any)["order_line_id"] == nil
	switch {
	case want == "draft" || v["state"] == "posted" || v["state"] == "cancelled":
		return
	case want == "cancelled":
		s.act(s.maker, path+"/cancel", nil)
		return
	}
	if v["state"] == "draft" && !expense {
		v = s.act(s.maker, path+"/match", nil)
	}
	if want == "matched" {
		return
	}
	if (v["state"] == "mismatch" || (expense && v["state"] == "draft")) && v["difference_approved_by"] == nil {
		reason := prefix + "供应商调价，已与采购确认"
		if expense {
			reason = prefix + "物业费按合同，无采购订单"
		}
		s.act(s.lead, path+"/approve-difference", map[string]any{"reason": reason})
	}
	s.act(s.maker, path+"/post", nil)
}

// manualItems registers manual open items as the maker: a receivable and a payable approved, a receivable waiting for
// approval, a payable cancelled.
func (s *seeder) manualItems() int {
	items := []struct{ kind, what, counter, want string }{
		{"receivable", "设备租赁收入（无销售订单）", "6051", "approved"}, {"payable", "外包审计服务费（无采购订单）", "6602", "approved"},
		{"receivable", "代垫运费，待客户确认", "6051", "waiting"}, {"payable", "重复登记的咨询费", "6602", "cancelled"},
	}
	for k, it := range items {
		partner := s.e.customers[k%len(s.e.customers)]
		if it.kind == "payable" {
			partner = s.e.suppliers[k%len(s.e.suppliers)]
		}
		body := map[string]any{"legal_entity_id": s.e.id, "kind": it.kind, "partner_id": partner, "currency": s.e.currency,
			"amount": fmt.Sprintf("%d.50", 1200+k*730), "due_date": inDays(30), "description": prefix + it.what,
			"counter_account_code": it.counter}
		o := s.maker.must("POST", "/api/finance/open-items", body, "Idempotency-Key", s.key("item", k))
		path := "/api/finance/open-items/" + o["id"].(string)
		if s.seed.must("GET", path, nil)["state"] != "pending_approval" {
			continue
		}
		switch it.want {
		case "approved":
			s.act(s.lead, path+"/approve", nil)
		case "cancelled":
			s.act(s.maker, path+"/cancel", nil)
		}
	}
	return len(items)
}
