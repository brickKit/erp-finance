//go:build integration

package itest

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vouchers: manual vouchers (docs/design.md, the voucher table; VC01-VC04): balanced, on postable active accounts,
// approved by another than their maker, numbered without a gap when posted, reversed by a reversing voucher; the
// balances follow.
func (fx *fixture) vouchers() {
	t := fx.t
	today := fx.today()
	// VC01: debits equal credits, on postable active accounts only
	for _, c := range []struct {
		lines  []any
		code   int
		reason string
	}{
		{fx.lines("6602", "1002", "100", "99"), 400, "VOUCHER_UNBALANCED"},
		{fx.lines("1000", "1002", "100", "100"), 409, "ACCOUNT_NOT_POSTABLE"},
		{fx.lines("6603", "1002", "100", "100"), 409, "ACCOUNT_NOT_POSTABLE"},
	} {
		code, out := fx.alice.do("POST", "/api/finance/vouchers", fx.voucherBody(today, c.lines))
		assert.Equal(t, c.code, code, out)
		assert.Equal(t, c.reason, out["reason"], c.lines)
	}

	// VC01: a draft changes by version; VC02: submitted, not approved by its maker, approved by another; VC03: numbered
	v := fx.voucher(today, fx.lines("6602", "1002", "100", "100"))
	assert.Equal(t, "draft", v["state"])
	assert.Nil(t, v["number"])
	id := v["id"].(string)
	code, out := fx.alice.do("PATCH", "/api/finance/vouchers/"+id, fx.with(fx.voucherBody(today, fx.lines("6602", "1002", "120", "120")), "version", 1))
	require.Equal(t, 200, code, out)
	assert.Equal(t, "120", at(out, "lines", 0, "amount"))
	fx.voucherAction(fx.alice, id, "submit", nil, 200)
	fx.voucherRefused(fx.alice, "PATCH", id, "", fx.voucherBody(today, fx.lines("6602", "1002", "1", "1")))
	fx.voucherRefused(fx.alice, "POST", id, "submit", nil)
	code, out = fx.alice.do("POST", "/api/finance/vouchers/"+id+"/approve", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "APPROVER_IS_SUBMITTER", out["reason"])
	v = fx.voucherAction(fx.bob, id, "approve", nil, 200)
	assert.Equal(t, "posted", v["state"])
	ym := today[:4] + today[5:7]
	assert.Equal(t, "JV-SH-"+ym+"-000001", v["number"])
	assert.Equal(t, today[:7], v["period"])
	assert.Equal(t, v["version"], fx.voucherAction(fx.bob, id, "approve", nil, 200)["version"]) // a repeat is ignored
	fx.voucherRefused(fx.bob, "POST", id, "reject", map[string]any{"reason": "late"})

	// VC02: rejected back to draft with its reason; then approved: the next number, no gap
	w := fx.voucher(today, fx.lines("1002", "6001", "300", "300"))
	wid := w["id"].(string)
	fx.voucherRefused(fx.bob, "POST", wid, "approve", nil)
	fx.voucherAction(fx.alice, wid, "submit", nil, 200)
	code, out = fx.bob.do("POST", "/api/finance/vouchers/"+wid+"/reject", map[string]any{})
	assert.Equal(t, 400, code, out)
	w = fx.voucherAction(fx.bob, wid, "reject", map[string]any{"reason": "附件不全"}, 200)
	assert.Equal(t, "draft", w["state"])
	assert.Equal(t, "附件不全", w["reason"])
	fx.voucherAction(fx.alice, wid, "submit", nil, 200)
	w = fx.voucherAction(fx.bob, wid, "approve", nil, 200)
	assert.Equal(t, "JV-SH-"+ym+"-000002", w["number"])

	// VC04: a reversing voucher, the opposite sides, numbered next; the original reversed, not reversed twice
	r := fx.voucherAction(fx.bob, id, "reverse", map[string]any{"reason": "金额录错"}, 200)
	assert.Equal(t, "posted", r["state"])
	assert.Equal(t, id, r["reverses_voucher_id"])
	assert.Equal(t, "credit", at(r, "lines", 0, "side"))
	assert.Equal(t, "JV-SH-"+ym+"-000003", r["number"])
	v = fx.get("/api/finance/vouchers/" + id)
	assert.Equal(t, "reversed", v["state"])
	assert.Equal(t, r["id"], v["reversed_by_id"])
	fx.voucherAction(fx.bob, id, "reverse", map[string]any{"reason": "again"}, 200) // ignored: already reversed
	assert.Equal(t, "reversed", fx.get("/api/finance/vouchers/" + id)["state"])
	fx.voucherRefused(fx.alice, "POST", wid, "submit", nil)

	// the balances of the month: 6602 debit 120 and credit 120 (reversed); 1002 debit 300 + 120 (the reversal), credit
	// 120; 6001 credit 300 (a credit balance is negative); a draft and a submitted voucher do not count
	fx.voucher(today, fx.lines("6602", "1002", "7", "7"))
	waiting := fx.voucher(today, fx.lines("6602", "1002", "8", "8"))["id"].(string)
	fx.voucherAction(fx.alice, waiting, "submit", nil, 200)
	bal := fx.balances(fx.le, today[:7])
	assert.Equal(t, []string{"0", "120", "120", "0"}, bal["6602"])
	assert.Equal(t, []string{"0", "420", "120", "300"}, bal["1002"])
	assert.Equal(t, []string{"0", "0", "300", "-300"}, bal["6001"])
	// the posted vouchers are facts: one event each
	assert.Equal(t, w["number"], fx.fact(wid, "erp.voucher.posted.v1")["number"])
	l := fx.get("/api/finance/vouchers?state=posted&legal_entity_id=" + fx.le)
	assert.Len(t, l["vouchers"], 2, l)
	// none of this month waits: the periods step closes it for its earlier open month alone
	fx.voucherAction(fx.bob, waiting, "reject", map[string]any{"reason": "不记了"}, 200)
}

// periods: a past month (PD01, PD02): closed with no voucher waiting; then a manual voucher there is refused, a
// reopened month takes it again.
func (fx *fixture) periods() {
	t := fx.t
	past := month(-2)
	v := fx.voucher(past, fx.lines("6602", "1002", "10", "10"))
	fx.voucherAction(fx.alice, v["id"].(string), "submit", nil, 200)
	period := past[:7]
	code, out := fx.bob.do("POST", "/api/finance/periods/"+fx.le+"/"+period+"/close", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "STATE_CONFLICT", out["reason"], "a manual voucher waits for approval")
	fx.voucherAction(fx.bob, v["id"].(string), "approve", nil, 200)
	p := fx.periodAction(period, "close", nil, 200)
	assert.Equal(t, "closed", p["state"])
	fx.periodAction(period, "close", nil, 200) // a repeat is ignored

	w := fx.voucher(past, fx.lines("6602", "1002", "5", "5"))
	fx.voucherAction(fx.alice, w["id"].(string), "submit", nil, 200)
	code, out = fx.bob.do("POST", "/api/finance/vouchers/"+w["id"].(string)+"/approve", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "PERIOD_CLOSED", out["reason"])

	code, out = fx.bob.do("POST", "/api/finance/periods/"+fx.le+"/"+period+"/reopen", map[string]any{})
	assert.Equal(t, 400, code, out)
	p = fx.periodAction(period, "reopen", map[string]any{"reason": "补记漏掉的费用"}, 200)
	assert.Equal(t, "reopened", p["state"])
	assert.Equal(t, float64(2), p["revision"])
	w = fx.voucherAction(fx.bob, w["id"].(string), "approve", nil, 200)
	assert.Equal(t, "posted", w["state"])
	fx.periodAction(period, "close", nil, 200)

	// the month before it was never posted to; a later month cannot close while an earlier one is open
	cur := fx.today()[:7]
	code, out = fx.bob.do("POST", "/api/finance/periods/"+fx.le+"/"+cur+"/close", nil)
	assert.Equal(t, 409, code, out, "the month between is open")
	l := fx.get("/api/finance/periods?legal_entity_id=" + fx.le)
	states := map[string]string{}
	for _, x := range l["periods"].([]any) {
		states[at(x, "period").(string)] = at(x, "state").(string)
	}
	assert.Equal(t, "closed", states[period])
	assert.Equal(t, "open", states[cur])
	assert.Equal(t, "closed", fx.lastPeriodEvent(period))
}

func (fx *fixture) lines(debit, credit, d, c string) []any {
	return []any{map[string]any{"account_code": debit, "side": "debit", "amount": d},
		map[string]any{"account_code": credit, "side": "credit", "amount": c}}
}

func (fx *fixture) voucherBody(date string, lines []any) map[string]any {
	return map[string]any{"legal_entity_id": fx.le, "business_date": date, "description": "演示凭证", "lines": lines}
}

func (fx *fixture) voucher(date string, lines []any) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("POST", "/api/finance/vouchers", fx.voucherBody(date, lines))
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) voucherAction(c client, id, action string, body map[string]any, want int) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/finance/vouchers/"+id+"/"+action, body)
	require.Equal(fx.t, want, code, out)
	return out
}

func (fx *fixture) voucherRefused(c client, method, id, action string, body map[string]any) {
	fx.t.Helper()
	path := "/api/finance/vouchers/" + id
	if action != "" {
		path += "/" + action
	}
	code, out := c.do(method, path, body)
	assert.Equal(fx.t, 409, code, "%s %s: %v", method, path, out)
	assert.Equal(fx.t, "STATE_CONFLICT", out["reason"], "%s %s", method, path)
}

func (fx *fixture) periodAction(period, action string, body map[string]any, want int) map[string]any {
	fx.t.Helper()
	code, out := fx.bob.do("POST", "/api/finance/periods/"+fx.le+"/"+period+"/"+action, body)
	require.Equal(fx.t, want, code, out)
	return out
}

// balances are each account's opening, debit, credit and closing of a month.
func (fx *fixture) balances(le, period string) map[string][]string {
	fx.t.Helper()
	out := map[string][]string{}
	for _, b := range fx.get("/api/finance/balances?legal_entity_id=" + le + "&period=" + period)["balances"].([]any) {
		out[at(b, "account_code").(string)] = []string{at(b, "opening").(string), at(b, "debit").(string),
			at(b, "credit").(string), at(b, "closing").(string)}
	}
	return out
}

func (fx *fixture) lastPeriodEvent(period string) string {
	fx.t.Helper()
	var state string
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT convert_from(payload, 'UTF8')::jsonb->>'state' FROM "+itSchema+
		".be_outbox WHERE type = 'erp.accounting_period.changed.v1' AND convert_from(payload, 'UTF8')::jsonb->>'period' = $1 "+
		"ORDER BY ord DESC LIMIT 1", period).Scan(&state))
	return state
}
