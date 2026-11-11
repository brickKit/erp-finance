//go:build integration

package itest

import (
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const accountingFact = "erp.payment.accounting_fact.v1"

// accountingFacts: a source's accounting facts posted by the rule and the legal entity's mapping (docs/design.md,
// AF01, AF02; protocol 05's accounting fact contract).
func (fx *fixture) accountingFacts() {
	t := fx.t
	// AF02: a rule the legal entity has not mapped waits; mapped, it posts
	charge := fx.accounting("bank_charge", map[string]any{"expense": "15", "bank": "15"}, "CNY", nil, fx.today())
	fx.publishFact(paymentStream, accountingFact, core.Payment, charge["fact_id"].(string), charge)
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, "pending", fx.receivedState(charge["fact_id"].(string)))
	m := fx.get("/api/finance/account-mappings/" + fx.le)
	m["entries"] = append(m["entries"].([]any), map[string]any{"rule_key": "bank_charge", "role": "expense", "account_code": "6603.1"},
		map[string]any{"rule_key": "bank_charge", "role": "bank", "account_code": "1002"},
		map[string]any{"rule_key": "bank_interest", "role": "bank", "account_code": "1002"},
		map[string]any{"rule_key": "bank_interest", "role": "income", "account_code": "6001"})
	fx.account("6603.1", "手续费", "expense", "")
	code, out := fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, m)
	require.Equal(t, 200, code, out)

	// AF01: the charge posted on the mapped accounts, its department and partner on every line, by erp/payment
	fx.posted(charge["fact_id"].(string))
	v := fx.voucherOf(charge["fact_id"].(string))
	fx.assertLines(charge["fact_id"].(string), map[string]string{"6603.1 debit": "15", "1002 credit": "15"})
	for _, l := range v["lines"].([]any) {
		assert.Equal(t, fx.d1, at(l, "department_id"))
		assert.Equal(t, fx.supplier, at(l, "partner_id"))
	}
	assert.Equal(t, "accounting_fact", at(v, "source", "kind"))
	assert.Nil(t, v["maker_id"])

	// AF01: interest in USD at its snapshot, its posting date (two months back, closed) postponed (PD01)
	interest := fx.accounting("bank_interest", map[string]any{"bank": "10", "income": "10"}, "USD",
		map[string]any{"rate": "7.2", "date": month(-2), "type": "spot"}, month(-2))
	fx.publishFact(paymentStream, accountingFact, core.Payment, interest["fact_id"].(string), interest)
	fx.posted(interest["fact_id"].(string))
	vi := fx.voucherOf(interest["fact_id"].(string))
	assert.Equal(t, month(-1)[:7]+"-01", vi["posting_date"])
	assert.Equal(t, "72", at(vi, "lines", 0, "base_amount"))

	// AF01, AF02: a transfer is a pair (protocol 05): its going out puts the money in transit, its arrival takes it
	// out again; one transfer document is the source of both
	rules := map[string]any{}
	for _, r := range fx.get("/api/finance/posting-rules")["rules"].([]any) {
		rules[at(r, "rule_key").(string)] = at(r, "roles")
	}
	assert.Equal(t, map[string]any{"in_transit": "debit", "bank": "credit"}, rules["fund_transfer_out"])
	assert.Equal(t, map[string]any{"bank": "debit", "in_transit": "credit"}, rules["fund_transfer_in"])
	assert.NotContains(t, rules, "fund_transfer", "a transfer is never one fact")
	// the cost rules erp/costing posts under (C5): an adjustment up or down is two rules, amounts are never negative
	for rule, roles := range map[string]map[string]any{
		"inventory_receipt":    {"inventory": "debit", "grni": "credit"},
		"cost_of_sales":        {"cost_of_sales": "debit", "inventory": "credit"},
		"cost_of_sales_return": {"inventory": "debit", "cost_of_sales": "credit"},
		"inventory_return":     {"grni": "debit", "inventory": "credit"},
		"cost_adjustment_up":   {"inventory": "debit", "variance": "debit", "grni": "credit"},
		"cost_adjustment_down": {"grni": "debit", "inventory": "credit", "variance": "credit"},
	} {
		assert.Equal(t, roles, rules[rule], rule)
	}
	fx.account("1012", "在途资金", "asset", "")
	fx.account("1002.2", "银行存款-工行", "asset", "")
	m = fx.get("/api/finance/account-mappings/" + fx.le)
	m["entries"] = append(m["entries"].([]any),
		map[string]any{"rule_key": "fund_transfer_out", "role": "in_transit", "account_code": "1012"},
		map[string]any{"rule_key": "fund_transfer_out", "role": "bank", "account_code": "1002"},
		map[string]any{"rule_key": "fund_transfer_in", "role": "bank", "account_code": "1002.2"},
		map[string]any{"rule_key": "fund_transfer_in", "role": "in_transit", "account_code": "1012"})
	code, out = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, m)
	require.Equal(t, 200, code, out)
	out1 := fx.accounting("fund_transfer_out", map[string]any{"in_transit": "1000", "bank": "1000"}, "CNY", nil, fx.today())
	in1 := fx.accounting("fund_transfer_in", map[string]any{"bank": "1000", "in_transit": "1000"}, "CNY", nil, fx.today())
	in1["source"] = out1["source"]
	for _, f := range []map[string]any{out1, in1} {
		fx.publishFact(paymentStream, accountingFact, core.Payment, f["fact_id"].(string), f)
		fx.posted(f["fact_id"].(string))
	}
	fx.assertLines(out1["fact_id"].(string), map[string]string{"1012 debit": "1000", "1002 credit": "1000"})
	fx.assertLines(in1["fact_id"].(string), map[string]string{"1002.2 debit": "1000", "1012 credit": "1000"})

	// AF02: a rule key the catalog has not waits; an unbalanced fact is not posted
	unknown := fx.accounting("payroll_lock", map[string]any{"salary": "1"}, "CNY", nil, fx.today())
	fx.publishFact(paymentStream, accountingFact, core.Payment, unknown["fact_id"].(string), unknown)
	unbalanced := fx.accounting("bank_charge", map[string]any{"expense": "15", "bank": "14"}, "CNY", nil, fx.today())
	fx.publishFact(paymentStream, accountingFact, core.Payment, unbalanced["fact_id"].(string), unbalanced)
	time.Sleep(2 * time.Second)
	assert.Equal(t, "pending", fx.receivedState(unknown["fact_id"].(string)))
	assert.Equal(t, "pending", fx.receivedState(unbalanced["fact_id"].(string)))
	var n int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".voucher WHERE source_id = ANY($1)",
		[]string{unknown["fact_id"].(string), unbalanced["fact_id"].(string)}).Scan(&n))
	assert.Zero(t, n)
}

// accounting is an accounting fact of Shanghai as erp/payment publishes it (the shared contract).
func (fx *fixture) accounting(rule string, amounts map[string]any, currency string, rate map[string]any, date string) map[string]any {
	id := ids.New().String()
	return map[string]any{"fact_id": id, "source": map[string]any{"component": core.Payment, "document_type": rule,
		"document_id": ids.New().String(), "document_number": "BK-" + id[:8]}, "legal_entity_id": fx.le,
		"business_date": date, "posting_date": date, "rule_key": rule, "currency": currency, "exchange_rate": rate,
		"amounts": amounts, "partner_id": fx.supplier, "dimensions": map[string]any{"department_id": fx.d1, "project_id": nil},
		"service_period": nil, "recorded_at": "2026-11-10T08:00:00.000000Z"}
}
