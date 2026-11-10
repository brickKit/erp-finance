//go:build integration

package itest

import (
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manualItems: open items registered by hand where no source exists (docs/design.md, the manual open item table;
// MI01-MI03): approved by another than the maker, posted, then claimable and settleable like any.
func (fx *fixture) manualItems() {
	t := fx.t
	today := fx.today()
	body := func(kind, amount, counter string) map[string]any {
		return map[string]any{"legal_entity_id": fx.le, "kind": kind, "partner_id": fx.customer, "currency": "CNY",
			"amount": amount, "due_date": today, "description": "补登的往来", "counter_account_code": counter,
			"department_id": fx.d1}
	}
	// MI01: a heading as the counter account, a foreign currency, a suspended partner are refused
	for _, c := range []struct {
		body   map[string]any
		reason string
	}{
		{body("receivable", "1", "1000"), "ACCOUNT_NOT_POSTABLE"},
		{fx.with(body("receivable", "1", "6001"), "currency", "USD"), "CURRENCY_NOT_USABLE"},
		{fx.with(body("receivable", "1", "6001"), "partner_id", fx.blocked), "PARTNER_NOT_USABLE"},
	} {
		code, out := fx.alice.do("POST", "/api/finance/open-items", c.body)
		assert.Equal(t, 409, code, out)
		assert.Equal(t, c.reason, out["reason"])
	}

	// MI01: registered, waiting for approval: not claimable
	r := fx.manual(body("receivable", "300", "6001"))
	assert.Equal(t, "pending_approval", r["state"])
	assert.Equal(t, "manual", r["origin"])
	assert.True(t, strings.HasPrefix(at(r, "source", "number").(string), "MI-SH-"+today[:4]+"-"), r["source"])
	p := fx.manual(fx.with(body("payable", "200", "6602"), "partner_id", fx.supplier))
	pay := fx.claimClient(core.Payment)
	fx.claimRefused(pay, ids.New().String(), p["id"].(string), "1", "CLAIM_STATE")

	// MI02: not approved by its maker, nor at another version; approved by Bob: posted, open, the receivable formed
	code, out := fx.alice.do("POST", "/api/finance/open-items/"+r["id"].(string)+"/approve", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "APPROVER_IS_SUBMITTER", out["reason"])
	code, out = fx.bob.do("POST", "/api/finance/open-items/"+r["id"].(string)+"/approve", map[string]any{"version": 99})
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "VERSION_CONFLICT", out["reason"])
	// MI02: the receivable's and the payable's accounts come from the mapping
	code, out = fx.bob.do("POST", "/api/finance/open-items/"+r["id"].(string)+"/approve", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "MAPPING_MISSING", out["reason"])
	m := fx.get("/api/finance/account-mappings/" + fx.le)
	m["entries"] = append(m["entries"].([]any), map[string]any{"rule_key": "manual_receivable", "role": "receivable",
		"account_code": "1122"}, map[string]any{"rule_key": "manual_payable", "role": "payable", "account_code": "2202"})
	code, out = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, m)
	require.Equal(t, 200, code, out)
	r = fx.manualAction(fx.bob, r["id"].(string), "approve")
	assert.Equal(t, "open", r["state"])
	fx.assertLines(r["id"].(string), map[string]string{"1122 debit": "300", "6001 credit": "300"})
	assert.Equal(t, "300", fx.fact(r["id"].(string), core.ReceivableFormed)["amount"])
	assert.Equal(t, r["version"], fx.manualAction(fx.bob, r["id"].(string), "approve")["version"]) // ignored
	p = fx.manualAction(fx.bob, p["id"].(string), "approve")
	fx.assertLines(p["id"].(string), map[string]string{"6602 debit": "200", "2202 credit": "200"})
	fx.noFact(p["id"].(string), core.ReceivableFormed)
	// approved, the payable is claimable like any
	c := ids.New().String()
	fx.claim(pay, c, p["id"].(string), "200")
	_, err := pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c})
	require.NoError(t, err)

	// MI03: a pending item cancels (again: ignored); a cancelled one is not approved; an open one does not cancel
	x := fx.manual(body("receivable", "5", "6001"))
	assert.Equal(t, "cancelled", fx.manualAction(fx.alice, x["id"].(string), "cancel")["state"])
	fx.manualAction(fx.alice, x["id"].(string), "cancel")
	for _, c := range [][2]string{{x["id"].(string), "approve"}, {r["id"].(string), "cancel"}} {
		code, out := fx.bob.do("POST", "/api/finance/open-items/"+c[0]+"/"+c[1], nil)
		assert.Equal(t, 409, code, out)
		assert.Equal(t, "STATE_CONFLICT", out["reason"], c[1])
	}
}

func (fx *fixture) manual(body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("POST", "/api/finance/open-items", body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) manualAction(c client, id, action string) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/finance/open-items/"+id+"/"+action, nil)
	require.Equal(fx.t, 200, code, out)
	return out
}
