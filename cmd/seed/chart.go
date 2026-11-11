package main

import (
	"fmt"
	"sort"
	"time"
)

// account is one account of the demo chart; a heading (not postable) is listed before the accounts below it.
type account struct{ code, name, category, parent string }

// demoChart is a generic demo chart, the same codes in every legal entity: not any country's standard chart.
var demoChart = []account{
	{"1000", "资产", "asset", ""}, {"1001", "库存现金", "asset", "1000"}, {"1002", "银行存款", "asset", "1000"},
	{"1012", "在途资金", "asset", "1000"}, {"1122", "应收账款", "asset", "1000"}, {"1221", "其他应收款", "asset", "1000"},
	{"1401", "材料采购", "asset", "1000"}, {"1405", "库存商品", "asset", "1000"}, {"1601", "固定资产", "asset", "1000"},
	{"2000", "负债", "liability", ""}, {"2202", "应付账款", "liability", "2000"}, {"2211", "应付职工薪酬", "liability", "2000"},
	{"2221", "应交税费", "liability", "2000"}, {"2221.01", "应交税费-进项税额", "liability", "2221"},
	{"2221.02", "应交税费-销项税额", "liability", "2221"}, {"2241", "其他应付款", "liability", "2000"},
	{"2249", "暂收款项（待查明）", "liability", "2000"},
	{"4000", "所有者权益", "equity", ""}, {"4001", "实收资本", "equity", "4000"}, {"4103", "本年利润", "equity", "4000"},
	{"6001", "主营业务收入", "income", ""}, {"6051", "其他业务收入", "income", ""}, {"6401", "主营业务成本", "expense", ""},
	{"6601", "销售费用", "expense", ""}, {"6602", "管理费用", "expense", ""}, {"6603", "财务费用", "expense", ""},
}

// headings are the accounts other accounts sit below.
var headings = map[string]bool{"1000": true, "2000": true, "2221": true, "4000": true}

// roleAccounts maps every posting rule's role to the demo chart; counter (the manual open items) is the one each item
// names, so it is not mapped.
var roleAccounts = map[string]string{"receivable": "1122", "revenue": "6001", "output_tax": "2221.02", "purchase": "1401",
	"input_tax": "2221.01", "payable": "2202", "bank": "1002", "suspense": "2249", "in_transit": "1012",
	"expense": "6603", "income": "6603"}

// chart makes the legal entity's missing accounts and sets the mapping when it differs.
func chart(c client, e *entity) {
	have := map[string]string{}
	code, out, err := c.UntilGranted(30*time.Second, "GET", "/api/finance/accounts?limit=200&legal_entity_id="+e.id, nil)
	check(err)
	if code != 200 {
		fail("accounts of %s: %d %v", e.code, code, out)
	}
	for _, a := range c.pages("/api/finance/accounts?legal_entity_id="+e.id, "accounts") {
		have[a["code"].(string)] = a["id"].(string)
	}
	made := 0
	for _, a := range demoChart {
		if have[a.code] != "" {
			continue
		}
		body := map[string]any{"legal_entity_id": e.id, "code": a.code, "name": a.name, "category": a.category}
		if a.parent != "" {
			body["parent_id"] = have[a.parent]
		}
		if headings[a.code] {
			body["postable"] = false
		}
		have[a.code] = c.must("POST", "/api/finance/accounts", body, "Idempotency-Key",
			"seed-finance-account-"+e.code+"-"+a.code)["id"].(string)
		made++
	}
	n := mapping(c, e)
	fmt.Printf("seed erp/finance: %s: %d accounts made, mapping %s\n", e.code, made, n)
}

// mapping sets every rule's roles (except counter) to the demo chart, when the current mapping differs.
func mapping(c client, e *entity) string {
	var want []string
	var entries []any
	for _, r := range c.must("GET", "/api/finance/posting-rules", nil)["rules"].([]any) {
		r := r.(map[string]any)
		for role := range r["roles"].(map[string]any) {
			if role == "counter" {
				continue
			}
			acc := roleAccounts[role]
			if acc == "" {
				fail("the posting rule %s has a role %s the seed does not map", r["rule_key"], role)
			}
			want = append(want, r["rule_key"].(string)+"/"+role+"="+acc)
			entries = append(entries, map[string]any{"rule_key": r["rule_key"], "role": role, "account_code": acc})
		}
	}
	cur := c.must("GET", "/api/finance/account-mappings/"+e.id, nil)
	var have []string
	for _, x := range cur["entries"].([]any) {
		x := x.(map[string]any)
		have = append(have, fmt.Sprintf("%s/%s=%s", x["rule_key"], x["role"], x["account_code"]))
	}
	sort.Strings(want)
	sort.Strings(have)
	if fmt.Sprint(want) == fmt.Sprint(have) {
		return "unchanged"
	}
	c.must("PUT", "/api/finance/account-mappings/"+e.id, map[string]any{"legal_entity_id": e.id, "version": cur["version"],
		"entries": entries})
	return "set"
}

// receivables waits until every issued invoice of erp/billing has formed its receivable here: the facts waited for
// the mapping (their retries back off to 5 minutes).
func receivables(c client, e *entity, issued int) int {
	deadline := time.Now().Add(7 * time.Minute)
	for {
		n := 0
		for _, o := range c.pages("/api/finance/open-items?kind=receivable&legal_entity_id="+e.id, "open_items") {
			if o["origin"] == "invoice" {
				n++
			}
		}
		if n >= issued {
			return n
		}
		if time.Now().After(deadline) {
			fail("%s: %d of erp/billing's %d issued invoices formed a receivable after 7 minutes", e.code, n, issued)
		}
		time.Sleep(5 * time.Second)
	}
}
