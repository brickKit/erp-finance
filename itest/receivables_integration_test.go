//go:build integration

package itest

import (
	"encoding/json"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/ids"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	"github.com/cockroachdb/apd/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var billingStream = events.Stream{Name: core.BillingStream, Partitions: 4}

// receivables: erp/billing's issued invoices form receivables (docs/design.md, "应收、应付与核销"; protocol 05 step 2):
// one voucher by the rule sales_invoice and one open item per invoice, the receivable.formed fact; a redelivery forms
// nothing more; a foreign currency invoice converts at its snapshot; a late invoice in a closed month posts on the
// next open month's first day; a legal entity with no mapping keeps its invoice until it has one.
func (fx *fixture) receivables() {
	t := fx.t
	today := fx.today()
	require.NoError(t, fx.tr.EnsureStream(fx.ctx, billingStream))
	before := fx.balances(fx.le, today[:7])

	// AR01: a CNY invoice; due on its date (the invoice carries no due date); no department
	i1 := fx.invoice(fx.le, "CNY", nil, today, "1000.00", "130.00", "1130.00")
	o1 := fx.openItemOf(i1["fact_id"].(string))
	assert.Equal(t, "receivable", o1["kind"])
	assert.Equal(t, "invoice", o1["origin"])
	assert.Equal(t, fx.customer, o1["partner_id"])
	assert.Equal(t, "1130", o1["amount"])
	assert.Equal(t, "1130", o1["available"])
	assert.Equal(t, "1130", o1["open_balance"])
	assert.Equal(t, "0", o1["settled"])
	assert.Equal(t, today, o1["due_date"])
	assert.Equal(t, "open", o1["state"])
	assert.Nil(t, o1["department_id"])
	assert.Equal(t, i1["number"], at(o1, "source", "number"))
	formed := fx.fact(o1["id"].(string), core.ReceivableFormed)
	assert.Equal(t, i1["fact_id"], formed["invoice_id"])
	assert.Equal(t, "1130", formed["amount"])
	assert.Equal(t, i1["order_id"], formed["order_id"])
	v1 := fx.voucherOf(i1["fact_id"].(string))
	assert.Equal(t, "posted", v1["state"])
	assert.Equal(t, today, v1["posting_date"])
	assert.Equal(t, "sales_invoice", at(v1, "source", "kind"))
	assert.Nil(t, v1["maker_id"])

	// AR02: the same fact again (a new event ID), then the same ID with another content: nothing more is formed
	fx.publishInvoice(i1, i1["fact_id"].(string))
	changed := fx.with(i1, "totals", map[string]any{"net": "1.00", "tax": "0.00", "gross": "1.00"})
	fx.publishInvoice(changed, i1["fact_id"].(string))

	// AR04: a USD invoice at 7.1234: revenue 712.41, tax 92.60, the receivable 805.02 rounded alone but 805.01 so the
	// voucher balances (the rounding difference goes to the largest line)
	rate := map[string]any{"rate": "7.1234", "date": today, "type": "spot"}
	i2 := fx.invoice(fx.le, "USD", rate, today, "100.01", "13.00", "113.01", i1["fact_id"].(string))
	o2 := fx.openItemOf(i2["fact_id"].(string))
	assert.Equal(t, "USD", o2["currency"])
	assert.Equal(t, "113.01", o2["amount"])
	v2 := fx.voucherOf(i2["fact_id"].(string))
	base := map[string]string{}
	for _, l := range v2["lines"].([]any) {
		assert.Equal(t, "USD", at(l, "currency"))
		base[at(l, "account_code").(string)] = at(l, "base_amount").(string)
	}
	assert.Equal(t, map[string]string{"1122": "805.01", "6001": "712.41", "2221.02": "92.6"}, base)

	// the redelivery and the changed copy were received before i2 (the same partition): one open item, one voucher
	l := fx.get("/api/finance/open-items?legal_entity_id=" + fx.le + "&kind=receivable")
	assert.Len(t, l["open_items"], 2, l)
	after := fx.balances(fx.le, today[:7])
	assert.Equal(t, plus(cell(before, "1122", 1), "1130", "805.01"), cell(after, "1122", 1))
	assert.Equal(t, plus(cell(before, "6001", 2), "1000", "712.41"), cell(after, "6001", 2))

	// PD01: an invoice dated in the closed month two back posts on the first day of the month after it (open); its
	// business date stays
	past := month(-2)
	i3 := fx.invoice(fx.le, "CNY", nil, past, "50.00", "0.00", "50.00", i1["fact_id"].(string))
	v3 := fx.voucherOf(i3["fact_id"].(string))
	assert.Equal(t, past, v3["business_date"])
	assert.Equal(t, month(-1)[:7]+"-01", v3["posting_date"])
	assert.Equal(t, month(-1)[:7], v3["period"])
	assert.Equal(t, past, fx.openItemOf(i3["fact_id"].(string))["due_date"])

	// AR03: Beijing has no mapping: its invoice waits, then posts once Beijing maps its accounts
	i4 := fx.invoice(fx.le2, "CNY", nil, today, "10.00", "0.00", "10.00")
	var state string
	require.Eventually(t, func() bool {
		return fx.admin.QueryRow(fx.ctx, "SELECT state FROM "+itSchema+".fact_received WHERE fact_id = $1",
			i4["fact_id"]).Scan(&state) == nil
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(1500 * time.Millisecond) // at least one attempt finds no mapping
	assert.Equal(t, "pending", state)
	l = fx.get("/api/finance/open-items?legal_entity_id=" + fx.le2)
	assert.Empty(t, l["open_items"])
	fx.chartOf(fx.le2)
	o4 := fx.openItemOf(i4["fact_id"].(string))
	assert.Equal(t, fx.le2, o4["legal_entity_id"])

	// FactService.Read answers the facts by identity, an unknown one with an empty type
	got := fx.readFacts(o1["id"].(string), ids.New().String())
	assert.Equal(t, core.ReceivableFormed, got[0]["type"])
	assert.Equal(t, o1["id"], at(got[0], "data", "fact_id"))
	assert.Equal(t, "", got[1]["type"])
}

// invoice publishes erp.invoice.issued.v1 as erp/billing does and returns its payload; partition, when given, is an
// earlier invoice whose partition this one goes to (so it is received after that one).
func (fx *fixture) invoice(le, currency string, rate map[string]any, date, net, tax, gross string, partition ...string) map[string]any {
	id := ids.New().String()
	inv := map[string]any{"fact_id": id, "number": "INV-" + id[:8], "legal_entity_id": le, "customer_id": fx.customer,
		"currency": currency, "exchange_rate": rate, "source": map[string]any{"kind": "sales_order", "id": ids.New().String()},
		"order_id": ids.New().String(), "invoice_date": date, "lines": []any{map[string]any{"id": ids.New().String(),
			"line_no": 1, "source_line_id": ids.New().String(), "product": map[string]any{"id": ids.New().String(),
				"code": "P1", "name": "产品"}, "quantity": "1", "unit": "EA", "unit_price": net, "net": net, "tax_rate": "0.13",
			"tax": tax}}, "totals": map[string]any{"net": net, "tax": tax, "gross": gross}, "issued_at": "2026-11-10T08:00:00.000000Z"}
	record := id
	if len(partition) > 0 {
		record = partition[0]
	}
	fx.publishInvoice(inv, record)
	return inv
}

func (fx *fixture) publishInvoice(inv map[string]any, record string) {
	fx.publishFact(billingStream, core.InvoiceIssued, core.Billing, record, inv)
}

// publishFact publishes an incremental fact as its producer's forwarder would, on the record's partition.
func (fx *fixture) publishFact(stream events.Stream, typ, producer, record string, payload map[string]any) {
	data, _ := json.Marshal(payload)
	row := events.Row{EventID: ids.New().String(), Stream: stream.Name, Partition: stream.PartitionOf(record),
		Type: typ, OccurredAt: time.Now(), RecordID: record, Version: 1, Projection: events.Full,
		Payload: data, Seq: time.Now().UnixNano(), Epoch: events.Epoch{Generation: 1, Timeline: 1}}
	row.Digest = events.Digest(row.EventID, data)
	require.NoError(fx.t, fx.tr.Publish(fx.ctx, events.Message{Subject: events.Subject(stream.Name, row.Partition, row.Type),
		Headers: row.Headers(producer, producer+"-it"), Data: data}))
}

// openItemOf waits for the open item an invoice formed.
func (fx *fixture) openItemOf(invoice string) map[string]any {
	fx.t.Helper()
	var id string
	require.Eventually(fx.t, func() bool {
		return fx.admin.QueryRow(fx.ctx, "SELECT id::text FROM "+itSchema+".open_item WHERE source_id = $1", invoice).Scan(&id) == nil
	}, 20*time.Second, 50*time.Millisecond, "the open item of %s", invoice)
	return fx.get("/api/finance/open-items/" + id)
}

// voucherOf is the system voucher posted from a source's document.
func (fx *fixture) voucherOf(source string) map[string]any {
	fx.t.Helper()
	var id string
	require.Eventually(fx.t, func() bool {
		return fx.admin.QueryRow(fx.ctx, "SELECT id::text FROM "+itSchema+".voucher WHERE source_id = $1", source).Scan(&id) == nil
	}, 20*time.Second, 50*time.Millisecond, "the voucher of %s", source)
	return fx.get("/api/finance/vouchers/" + id)
}

// chartOf gives a legal entity the accounts and the mapping of the rule sales_invoice.
func (fx *fixture) chartOf(le string) {
	fx.t.Helper()
	for _, a := range [][2]string{{"1122", "asset"}, {"6001", "income"}, {"2221.02", "liability"}} {
		code, out := fx.alice.do("POST", "/api/finance/accounts", map[string]any{"legal_entity_id": le, "code": a[0],
			"name": a[0], "category": a[1]})
		require.Equal(fx.t, 200, code, out)
	}
	code, out := fx.alice.do("PUT", "/api/finance/account-mappings/"+le, map[string]any{"legal_entity_id": le,
		"version": 0, "entries": []any{
			map[string]any{"rule_key": "sales_invoice", "role": "receivable", "account_code": "1122"},
			map[string]any{"rule_key": "sales_invoice", "role": "revenue", "account_code": "6001"},
			map[string]any{"rule_key": "sales_invoice", "role": "output_tax", "account_code": "2221.02"}}})
	require.Equal(fx.t, 200, code, out)
}

// readFacts calls FactService.Read as erp/credit would.
func (fx *fixture) readFacts(factIDs ...string) []map[string]any {
	fx.t.Helper()
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(fx.t, "erp/credit").ClientConfig(core.ID))))
	require.NoError(fx.t, err)
	defer conn.Close()
	r, err := financev3.NewFactServiceClient(conn).Read(fx.ctx, &financev3.ReadRequest{FactIds: factIDs})
	require.NoError(fx.t, err)
	var out []map[string]any
	for _, f := range r.GetFacts() {
		m := map[string]any{"type": f.GetType(), "fact_id": f.GetFactId()}
		if len(f.GetData()) > 0 {
			var data map[string]any
			require.NoError(fx.t, json.Unmarshal(f.GetData(), &data))
			m["data"] = data
		}
		out = append(out, m)
	}
	return out
}

// plus adds decimal strings exactly (amounts in the tests have at most four places).
func plus(xs ...string) string {
	var sum apd.Decimal
	for _, x := range xs {
		d, _, err := apd.NewFromString(x)
		if err != nil {
			panic(err)
		}
		_, _ = apd.BaseContext.WithPrecision(40).Add(&sum, &sum, d)
	}
	sum.Reduce(&sum)
	return sum.Text('f')
}

// cell is an account's opening (0), debit (1), credit (2) or closing (3) in balances; "0" for an account with none.
func cell(balances map[string][]string, code string, i int) string {
	if b, ok := balances[code]; ok {
		return b[i]
	}
	return "0"
}
