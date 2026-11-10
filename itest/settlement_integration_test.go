//go:build integration

package itest

import (
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/ids"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var paymentStream = events.Stream{Name: "erp.payment", Partitions: 4}

const fundFact = "erp.payment.fund_fact.v1"

// settlement: erp/payment's fund facts settle open items in finance's local transaction (docs/design.md, ST01-ST05;
// protocol 05's settlement and payment claims; CL07, CL09): receipts allocated to the customer's earliest receivables,
// the rest a credit balance; a refund and a payment by their claims; reversals, one arriving before its fact; a receipt
// with no payer held in suspense.
func (fx *fixture) settlement() {
	t := fx.t
	require.NoError(t, fx.tr.EnsureStream(fx.ctx, paymentStream))
	pay := fx.claimClient(core.Payment)
	// the customer's CNY receivables: i3 50 (due two months back), i1 1130 (due today); i2 is USD
	var i1, i3 string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT (SELECT id::text FROM "+itSchema+".open_item WHERE kind = 'receivable' "+
		"AND amount = 1130), (SELECT id::text FROM "+itSchema+".open_item WHERE kind = 'receivable' AND amount = 50)").Scan(&i1, &i3))
	// Shanghai holds unknown payers in 2241 (suspense)
	fx.account("2241", "其他应付款", "liability", "2000")
	m := fx.get("/api/finance/account-mappings/" + fx.le)
	m["entries"] = append(m["entries"].([]any), map[string]any{"rule_key": "receipt", "role": "suspense", "account_code": "2241"},
		map[string]any{"rule_key": "refund", "role": "receivable", "account_code": "1122"},
		map[string]any{"rule_key": "refund", "role": "bank", "account_code": "1002"})
	code, out := fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, m)
	require.Equal(t, 200, code, out)

	// ST03, ST04: 500 received twice at once (one fact): i3 cleared, i1 450 settled; once only
	r1 := fx.fund("receipt", "500", &fx.customer, nil, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, r1["fact_id"].(string), r1)
	fx.publishFact(paymentStream, fundFact, core.Payment, r1["fact_id"].(string), r1)
	fx.posted(r1["fact_id"].(string))
	o3, o1 := fx.get("/api/finance/open-items/"+i3), fx.get("/api/finance/open-items/"+i1)
	assert.Equal(t, "settled", o3["state"])
	assert.Equal(t, "50", o3["settled"])
	assert.Equal(t, "450", o1["settled"])
	assert.Equal(t, "680", o1["open_balance"])
	assert.Len(t, o1["allocations"], 1)
	assert.Equal(t, "settle", at(o1, "allocations", 0, "direction"))
	assert.Equal(t, 2, fx.countFacts(core.OpenItemSettled, "fund_fact_id", r1["fact_id"].(string)))
	assert.Equal(t, "0", fx.factWhere(core.ReceivableCleared, "open_item_id", i3)["open_balance"])
	assert.Equal(t, "680", fx.factWhere(core.ReceivableReduced, "open_item_id", i1)["open_balance"])
	fx.assertLines(r1["fact_id"].(string), map[string]string{"1002 debit": "500", "1122 credit": "500"})

	// ST03: 800 more: i1 cleared, 120 left a credit balance (the USD receivable is not touched)
	r2 := fx.fund("receipt", "800", &fx.customer, nil, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, r2["fact_id"].(string), r2)
	fx.posted(r2["fact_id"].(string))
	assert.Equal(t, "settled", fx.get("/api/finance/open-items/"+i1)["state"])
	credit := fx.openItemOf(r2["fact_id"].(string))
	assert.Equal(t, "credit_balance", credit["kind"])
	assert.Equal(t, "120", credit["amount"])
	assert.Equal(t, "fund", credit["origin"])

	// CL07: a refund of 100 against a confirmed claim of the credit balance's 120: formalized at 100, 20 available again
	rc := ids.New().String()
	fx.claim(pay, rc, credit["id"].(string), "120")
	_, err := pay.Confirm(fx.ctx, &financev3.ConfirmRequest{ClaimId: rc})
	require.NoError(t, err)
	f1 := fx.fund("refund", "100", &fx.customer, []any{map[string]any{"claim_id": rc, "amount": "100"}}, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, f1["fact_id"].(string), f1)
	fx.posted(f1["fact_id"].(string))
	credit = fx.get("/api/finance/open-items/" + credit["id"].(string))
	assert.Equal(t, "100", credit["settled"])
	assert.Equal(t, "0", credit["pay_claimed"])
	assert.Equal(t, "20", credit["available"])
	g, err := pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: rc})
	require.NoError(t, err)
	assert.Equal(t, "formalized", g.GetClaim().GetState())
	assert.Equal(t, "100", g.GetClaim().GetFormalizedAmount())
	fx.assertLines(f1["fact_id"].(string), map[string]string{"1122 debit": "100", "1002 credit": "100"})

	// CL07: the protocol's example: a payable of 1585.2, 600 claimed and confirmed, 500 paid: 500 settled, 1085.2
	// available
	pc := ids.New().String()
	fx.claim(pay, pc, fx.payable, "600")
	_, err = pay.Confirm(fx.ctx, &financev3.ConfirmRequest{ClaimId: pc})
	require.NoError(t, err)
	p1 := fx.fund("payment", "500", &fx.supplier, []any{map[string]any{"claim_id": pc, "amount": "500"}}, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, p1["fact_id"].(string), p1)
	fx.posted(p1["fact_id"].(string))
	payable := fx.get("/api/finance/open-items/" + fx.payable)
	assert.Equal(t, "500", payable["settled"])
	assert.Equal(t, "0", payable["pay_claimed"])
	assert.Equal(t, "1085.2", payable["available"])
	fx.assertLines(p1["fact_id"].(string), map[string]string{"2202 debit": "500", "1002 credit": "500"})
	// ST03: a payment by an unknown claim posts but settles nothing
	p2 := fx.fund("payment", "10", &fx.supplier, []any{map[string]any{"claim_id": ids.New().String(), "amount": "10"}}, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, p2["fact_id"].(string), p2)
	fx.posted(p2["fact_id"].(string))
	assert.Equal(t, 0, fx.countFacts(core.OpenItemSettled, "fund_fact_id", p2["fact_id"].(string)))
	// ST03: nor by a claim not confirmed (the fact still posts; the claim stays pending)
	pending := ids.New().String()
	fx.claim(pay, pending, fx.payable, "5")
	p3 := fx.fund("payment", "5", &fx.supplier, []any{map[string]any{"claim_id": pending, "amount": "5"}}, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, p3["fact_id"].(string), p3)
	fx.posted(p3["fact_id"].(string))
	assert.Equal(t, 0, fx.countFacts(core.OpenItemSettled, "fund_fact_id", p3["fact_id"].(string)))
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: pending})
	require.NoError(t, err)

	// ST05, CL09: the payment reversed (twice: once): the payable back to 1585.2 open, the claim reversed, the voucher
	// mirrored
	v1 := fx.fund("reversal", "500", &fx.supplier, nil, p1["fact_id"])
	fx.publishFact(paymentStream, fundFact, core.Payment, v1["fact_id"].(string), v1)
	fx.publishFact(paymentStream, fundFact, core.Payment, v1["fact_id"].(string), v1)
	fx.posted(v1["fact_id"].(string))
	payable = fx.get("/api/finance/open-items/" + fx.payable)
	assert.Equal(t, "0", payable["settled"])
	assert.Equal(t, "1585.2", payable["available"])
	assert.Len(t, payable["allocations"], 2)
	g, err = pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: pc})
	require.NoError(t, err)
	assert.Equal(t, "500", g.GetClaim().GetReversedAmount())
	fx.assertLines(v1["fact_id"].(string), map[string]string{"2202 credit": "500", "1002 debit": "500"})

	// ST05: the first receipt reversed: i3 and i1 reopened
	v2 := fx.fund("reversal", "500", &fx.customer, nil, r1["fact_id"])
	fx.publishFact(paymentStream, fundFact, core.Payment, v2["fact_id"].(string), v2)
	fx.posted(v2["fact_id"].(string))
	assert.Equal(t, "50", fx.get("/api/finance/open-items/"+i3)["open_balance"])
	assert.Equal(t, "450", fx.get("/api/finance/open-items/"+i1)["open_balance"])
	assert.Equal(t, "450", fx.factWhere(core.ReceivableReopened, "open_item_id", i1)["open_balance"])
	assert.Equal(t, "50", fx.factWhere(core.ReceivableReopened, "open_item_id", i3)["open_balance"])

	// ST05: a reversal before its receipt waits; then the receipt (30, allocated to i3) and the reversal (i3 open again)
	r3id := ids.New().String()
	v3 := fx.fund("reversal", "30", &fx.customer, nil, r3id)
	fx.publishFact(paymentStream, fundFact, core.Payment, v3["fact_id"].(string), v3)
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, "pending", fx.receivedState(v3["fact_id"].(string)))
	r3 := fx.fund("receipt", "30", &fx.customer, nil, nil)
	r3["fact_id"] = r3id
	// i1's 450 and i3's 50 are open again: 30 goes to i3 (earliest)
	fx.publishFact(paymentStream, fundFact, core.Payment, r3id, r3)
	fx.posted(r3id)
	fx.posted(v3["fact_id"].(string))
	assert.Equal(t, "50", fx.get("/api/finance/open-items/"+i3)["open_balance"], "allocated, then reversed")

	// ST03, ST05: money from a partner with no receivable is its credit balance; reversed, the credit balance is cancelled
	r4 := fx.fund("receipt", "40", &fx.supplier, nil, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, r4["fact_id"].(string), r4)
	fx.posted(r4["fact_id"].(string))
	c4 := fx.openItemOf(r4["fact_id"].(string))
	assert.Equal(t, "40", c4["amount"])
	v4 := fx.fund("reversal", "40", &fx.supplier, nil, r4["fact_id"])
	fx.publishFact(paymentStream, fundFact, core.Payment, v4["fact_id"].(string), v4)
	fx.posted(v4["fact_id"].(string))
	assert.Equal(t, "cancelled", fx.get("/api/finance/open-items/"+c4["id"].(string))["state"])

	// ST02: a receipt with no payer: held in suspense, nothing allocated
	s1 := fx.fund("receipt", "70", nil, nil, nil)
	fx.publishFact(paymentStream, fundFact, core.Payment, s1["fact_id"].(string), s1)
	fx.posted(s1["fact_id"].(string))
	fx.assertLines(s1["fact_id"].(string), map[string]string{"1002 debit": "70", "2241 credit": "70"})
	assert.Equal(t, 0, fx.countFacts(core.OpenItemSettled, "fund_fact_id", s1["fact_id"].(string)))
}

// fund is a fund fact of Shanghai in CNY as erp/payment publishes it (the shared contract).
func (fx *fixture) fund(kind, amount string, partner *string, claims []any, reverses any) map[string]any {
	id := ids.New().String()
	if claims == nil {
		claims = []any{}
	}
	rules := map[string]string{"receipt": "receipt", "payment": "payment", "refund": "refund", "reversal": "receipt"}
	return map[string]any{"fact_id": id, "source": map[string]any{"component": core.Payment, "document_type": kind,
		"document_id": ids.New().String(), "document_number": "PAY-" + id[:8]}, "legal_entity_id": fx.le,
		"business_date": fx.today(), "posting_date": fx.today(), "rule_key": rules[kind], "currency": "CNY",
		"exchange_rate": nil, "dimensions": map[string]any{"department_id": nil, "project_id": nil},
		"recorded_at": "2026-11-10T08:00:00.000000Z", "kind": kind, "partner_id": partner,
		"company_account_id": ids.New().String(), "amount": amount, "order_ref": nil, "claims": claims,
		"reverses_fact_id": reverses}
}

// posted waits until a received fact is posted.
func (fx *fixture) posted(factID string) {
	fx.t.Helper()
	require.Eventually(fx.t, func() bool { return fx.receivedState(factID) == "posted" }, 20*time.Second,
		50*time.Millisecond, "fact %s posted", factID)
}

func (fx *fixture) receivedState(factID string) string {
	var state string
	_ = fx.admin.QueryRow(fx.ctx, "SELECT state FROM "+itSchema+".fact_received WHERE fact_id = $1", factID).Scan(&state)
	return state
}

func (fx *fixture) countFacts(typ, key, value string) int {
	fx.t.Helper()
	var n int
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = $1 AND "+
		"convert_from(payload, 'UTF8')::jsonb->>'"+key+"' = $2", typ, value).Scan(&n))
	return n
}

// assertLines checks the voucher posted from a source: account and side -> amount.
func (fx *fixture) assertLines(source string, want map[string]string) {
	fx.t.Helper()
	got := map[string]string{}
	for _, l := range fx.voucherOf(source)["lines"].([]any) {
		got[at(l, "account_code").(string)+" "+at(l, "side").(string)] = at(l, "amount").(string)
	}
	assert.Equal(fx.t, want, got, "the voucher of %s", source)
}
