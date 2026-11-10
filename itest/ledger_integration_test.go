//go:build integration

package itest

import (
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chart: Shanghai's chart of accounts (a small demo chart: headings that are not postable, leaves that are), the posting
// rules and the legal entity's mapping of rule key and role to its accounts. The steps after post through it.
func (fx *fixture) chart() {
	t := fx.t
	fx.accounts = map[string]map[string]any{}
	for _, a := range [][4]string{
		{"1000", "资产", "asset", ""}, {"1002", "银行存款", "asset", "1000"}, {"1122", "应收账款", "asset", "1000"},
		{"1401", "材料采购", "asset", "1000"}, {"2000", "负债", "liability", ""}, {"2202", "应付账款", "liability", "2000"},
		{"2221.01", "应交税费-进项税额", "liability", "2000"}, {"2221.02", "应交税费-销项税额", "liability", "2000"},
		{"6001", "主营业务收入", "income", ""}, {"6602", "管理费用", "expense", ""},
	} {
		fx.account(a[0], a[1], a[2], a[3])
	}
	code, out := fx.alice.do("POST", "/api/finance/accounts", map[string]any{"legal_entity_id": fx.le, "code": "1002",
		"name": "重复", "category": "asset"})
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "CODE_TAKEN", out["reason"])
	assert.Equal(t, false, fx.accounts["1000"]["postable"], "a heading with accounts below is not postable")
	l := fx.get("/api/finance/accounts?legal_entity_id=" + fx.le + "&category=liability")
	assert.Len(t, l["accounts"], 4, l)

	// the rules: each role's side; the mapping by version, to postable accounts only
	rules := fx.get("/api/finance/posting-rules")
	var sales map[string]any
	for _, r := range rules["rules"].([]any) {
		if at(r, "rule_key") == "sales_invoice" {
			sales = r.(map[string]any)
		}
	}
	require.NotNil(t, sales, rules)
	assert.Equal(t, map[string]any{"receivable": "debit", "revenue": "credit", "output_tax": "credit"}, sales["roles"])
	m := fx.get("/api/finance/account-mappings/" + fx.le)
	assert.Equal(t, float64(0), m["version"])
	code, out = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, fx.mapping(0, map[string]string{"receivable": "1000"}))
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "ACCOUNT_NOT_POSTABLE", out["reason"])
	code, out = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, map[string]any{"legal_entity_id": fx.le, "version": 0,
		"entries": []any{map[string]any{"rule_key": "no_such_rule", "role": "x", "account_code": "1002"}}})
	assert.Equal(t, 400, code, out)
	code, m = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, fx.mapping(0, nil))
	require.Equal(t, 200, code, m)
	assert.Equal(t, float64(1), m["version"])
	code, out = fx.alice.do("PUT", "/api/finance/account-mappings/"+fx.le, fx.mapping(0, nil))
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "VERSION_CONFLICT", out["reason"])

	// an account changes its name; an inactive one is not posted to (below)
	id := fx.accounts["6602"]["id"].(string)
	code, out = fx.alice.do("PATCH", "/api/finance/accounts/"+id, map[string]any{"name": "管理费用（演示）", "version": 1})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "管理费用（演示）", out["name"])
	inactive := fx.account("6603", "财务费用", "expense", "")
	code, out = fx.alice.do("PATCH", "/api/finance/accounts/"+inactive["id"].(string), map[string]any{"state": "inactive"})
	require.Equal(t, 200, code, out)
}

// mapping is Shanghai's mapping of every rule's roles, with some entries replaced.
func (fx *fixture) mapping(version int, replace map[string]string) map[string]any {
	accounts := map[string]string{"bank": "1002", "receivable": "1122", "revenue": "6001", "output_tax": "2221.02",
		"purchase": "1401", "input_tax": "2221.01", "payable": "2202"}
	for k, v := range replace {
		accounts[k] = v
	}
	var entries []any
	for rule, roles := range map[string][]string{"sales_invoice": {"receivable", "revenue", "output_tax"},
		"supplier_invoice": {"purchase", "input_tax", "payable"}, "receipt": {"bank", "receivable"},
		"payment": {"payable", "bank"}} {
		for _, role := range roles {
			entries = append(entries, map[string]any{"rule_key": rule, "role": role, "account_code": accounts[role]})
		}
	}
	return map[string]any{"legal_entity_id": fx.le, "version": version, "entries": entries}
}

func (fx *fixture) account(code, name, category, parent string) map[string]any {
	fx.t.Helper()
	body := map[string]any{"legal_entity_id": fx.le, "code": code, "name": name, "category": category}
	if parent != "" {
		body["parent_id"] = fx.accounts[parent]["id"]
		// a heading becomes not postable once an account is below it (read back after)
	}
	if code == "1000" || code == "2000" {
		body["postable"] = false
	}
	c, out := fx.alice.do("POST", "/api/finance/accounts", body)
	require.Equal(fx.t, 200, c, out)
	fx.accounts[code] = out
	return out
}

// month is the first day of the month n months from now in Shanghai (YYYY-MM-DD).
func month(n int) string {
	return time.Now().In(shanghai).AddDate(0, n, 0).Format("2006-01") + "-01"
}
