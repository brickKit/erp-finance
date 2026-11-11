// Command seed fills a running erp/finance with the seed data for people (be-assembly-standard's seed and test data
// conventions):
//
//   - access, by the authorization admin: the role maintain (the seed user: every operation in every legal entity and
//     department); test.accountant.cn, an accountant of CN01: makes vouchers, registers supplier invoices and manual
//     open items in CN01 (every department), reads the chart and the periods; test.finance.lead, the finance lead:
//     approves vouchers, invoice differences and manual open items and closes periods in CN01, ES01 and US01, reads
//     them all (an approver is never the maker: the accountant makes CN01's, the seed user ES01's and US01's);
//   - a demo chart of accounts for CN01, ES01 and US01 (the same codes; headings not postable) and the mapping of every
//     posting rule's roles; the receivables then form from erp/billing's issued invoices (their facts wait for the
//     mapping), and the seed waits for them;
//   - manual vouchers in every state over three months (drafts, submitted, rejected with the reason, posted, reversed):
//     CN01 has more than a page with the receivables and the supplier invoices; two months ago closed, last month
//     closed and reopened with the reason;
//   - supplier invoices against erp/purchase's completed orders in the base currency (matched and posted, a price
//     difference approved and posted, matched only, drafts, cancelled), expense invoices approved and posted;
//   - manual open items: approved receivables and payables, one waiting for approval, one cancelled.
//
// The documents are seeded once, into a legal entity without a reopened period (the last step); a run that broke off
// is finished by running it again within a day (the idempotency keys answer the requests already made, and each
// document only moves on from the state it is in). The chart and the mapping are made or set only when they differ,
// so a second run changes nothing. The legal entities, departments and partners come from mdm/org and mdm/partner,
// the orders from erp/purchase, the issued invoices from erp/billing (their seeds run first).
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the user that writes the data; the test accounts' password
//	ERP_FINANCE_URL (http://localhost:18087), MDM_ORG_URL (http://localhost:18083), MDM_PARTNER_URL (http://localhost:18225),
//	ERP_PURCHASE_URL (http://localhost:18085), ERP_BILLING_URL (http://localhost:18089)
package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const (
	prefix     = "「本地测试」"
	accountant = "test.accountant.cn"
	lead       = "test.finance.lead"
)

var entities = []string{"CN01", "ES01", "US01"}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed erp/finance: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

type client struct{ seed.Client }

func (c client) must(method, path string, body any, header ...string) map[string]any {
	out, err := c.Must(method, path, body, header...)
	check(err)
	return out
}

// pages reads every page of a list (path carries its query already).
func (c client) pages(path, key string) []map[string]any {
	var out []map[string]any
	cursor := ""
	for {
		page := c.must("GET", path+"&limit=200&cursor="+url.QueryEscape(cursor), nil)
		for _, x := range page[key].([]any) {
			out = append(out, x.(map[string]any))
		}
		if cursor, _ = page["next_cursor"].(string); cursor == "" {
			return out
		}
	}
}

// segment grants an operation on a finance resource in the legal entities (all when none); open items and supplier
// invoices carry the department too, granted whole.
func segment(op [2]string, legalEntities []string) sdkauthz.Segment {
	s := seed.Segment(op[0], op[1])
	le := sdkauthz.Atom{Kind: "all"}
	if len(legalEntities) > 0 {
		le = sdkauthz.Atom{Kind: "in", Values: legalEntities}
	}
	s.Atoms = map[string]sdkauthz.Atom{"legal_entity": le}
	if op[0] == "erp.finance.open_item" || op[0] == "erp.finance.supplier_invoice" {
		s.Atoms["department"] = sdkauthz.Atom{Kind: "all"}
	}
	return s
}

func segments(les []string, ops ...[2]string) []sdkauthz.Segment {
	var out []sdkauthz.Segment
	for _, op := range ops {
		out = append(out, segment(op, les))
	}
	return out
}

var (
	accRead    = [2]string{"erp.finance.account", "erp.finance.account.read"}
	accManage  = [2]string{"erp.finance.account", "erp.finance.account.manage"}
	vouRead    = [2]string{"erp.finance.voucher", "erp.finance.voucher.read"}
	vouEdit    = [2]string{"erp.finance.voucher", "erp.finance.voucher.edit"}
	vouApprove = [2]string{"erp.finance.voucher", "erp.finance.voucher.approve"}
	perRead    = [2]string{"erp.finance.period", "erp.finance.period.read"}
	perClose   = [2]string{"erp.finance.period", "erp.finance.period.close"}
	oiRead     = [2]string{"erp.finance.open_item", "erp.finance.open_item.read"}
	oiEdit     = [2]string{"erp.finance.open_item", "erp.finance.open_item.edit"}
	oiApprove  = [2]string{"erp.finance.open_item", "erp.finance.open_item.approve"}
	siRead     = [2]string{"erp.finance.supplier_invoice", "erp.finance.supplier_invoice.read"}
	siEdit     = [2]string{"erp.finance.supplier_invoice", "erp.finance.supplier_invoice.edit"}
	siApprove  = [2]string{"erp.finance.supplier_invoice", "erp.finance.supplier_invoice.approve"}
	everyOp    = [][2]string{accRead, accManage, vouRead, vouEdit, vouApprove, perRead, perClose, oiRead, oiEdit, oiApprove,
		siRead, siEdit, siApprove}
)

// entity is one legal entity and what the seed uses of it.
type entity struct {
	code, id, currency string
	depts              []string // active departments, by code
	customers          []string // partners with an active customer role here
	suppliers          []string // partners with an active supplier role here
}

func main() {
	base := env("ERP_FINANCE_URL", "http://localhost:18087")
	if !seed.Ready(base) {
		fail("erp/finance at %s is not ready", base)
	}
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	accounts, err := seed.Accounts(iam, pw, admin, user, accountant, lead)
	check(err)
	t := accounts[user].Token
	les := read(t)
	a, err := seed.NewAuthz(env("SEED_AUTHZ_URL", "http://localhost:18223"), accounts[admin].Token, "erp/finance")
	check(err)
	cn := []string{les["CN01"].id}
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "maintain", Name: prefix + "财务维护", To: []string{user}, Segments: segments(nil, everyOp...)},
		{Key: "accountant-cn", Name: prefix + "会计（CN01）", To: []string{accountant},
			Segments: segments(cn, accRead, vouRead, vouEdit, perRead, oiRead, oiEdit, siRead, siEdit)},
		{Key: "lead", Name: prefix + "财务主管（审批）", To: []string{lead},
			Segments: segments(nil, accRead, vouRead, vouApprove, perRead, perClose, oiRead, oiApprove, siRead, siApprove)},
	}, accounts)
	check(err)

	c := client{seed.NewClient(base, t)}
	actors := map[string]client{"seed": c, "accountant": {seed.NewClient(base, accounts[accountant].Token)},
		"lead": {seed.NewClient(base, accounts[lead].Token)}}
	for _, code := range entities {
		chart(c, les[code])
	}
	issued := issuedInvoices(t, les)
	for _, code := range entities {
		e := les[code]
		n := receivables(c, e, issued[code])
		if done(c, e) {
			fmt.Printf("seed erp/finance: %s has a reopened period already; the documents are not seeded again (%d receivables)\n",
				code, n)
			continue
		}
		s := &seeder{e: e, maker: actors["seed"], lead: actors["lead"], seed: c}
		if code == "CN01" {
			s.maker = actors["accountant"]
		}
		v := s.vouchers()
		si := s.supplierInvoices()
		mi := s.manualItems()
		s.periods()
		fmt.Printf("seed erp/finance: %s: %d receivables, %d manual vouchers, %d supplier invoices, %d manual open items\n",
			code, n, v, si, mi)
	}
}

// read gathers what the seed needs from the master data.
func read(t string) map[string]*entity {
	les := map[string]*entity{}
	byID := map[string]*entity{}
	org := client{seed.NewClient(env("MDM_ORG_URL", "http://localhost:18083"), t)}
	for _, x := range org.must("GET", "/api/org/legal-entities?limit=200", nil)["legal_entities"].([]any) {
		x := x.(map[string]any)
		e := &entity{code: x["code"].(string), id: x["id"].(string), currency: x["base_currency"].(string)}
		les[e.code], byID[e.id] = e, e
	}
	for _, code := range entities {
		e := les[code]
		if e == nil {
			fail("mdm/org has no legal entity %s: run its seed first", code)
		}
		for _, u := range org.pages("/api/org/units?state=active&legal_entity_id="+e.id, "units") {
			e.depts = append(e.depts, u["id"].(string))
		}
		if len(e.depts) == 0 {
			fail("mdm/org has no active department in %s: run its seed first", code)
		}
	}
	partner := client{seed.NewClient(env("MDM_PARTNER_URL", "http://localhost:18225"), t)}
	partners := partner.pages("/api/partner/partners?state=active", "partners")
	sort.Slice(partners, func(i, j int) bool { return partners[i]["name"].(string) < partners[j]["name"].(string) })
	for _, p := range partners {
		for _, r := range partner.must("GET", "/api/partner/partners/"+p["id"].(string)+"/roles", nil)["roles"].([]any) {
			r := r.(map[string]any)
			le, _ := r["legal_entity_id"].(string)
			e := byID[le]
			if e == nil || r["state"] != "active" {
				continue
			}
			switch r["role"] {
			case "customer":
				e.customers = append(e.customers, p["id"].(string))
			case "supplier":
				e.suppliers = append(e.suppliers, p["id"].(string))
			}
		}
	}
	for _, code := range entities {
		if len(les[code].customers) == 0 || len(les[code].suppliers) == 0 {
			fail("mdm/partner has no customer or supplier in %s: run its seed first", code)
		}
	}
	return les
}

// issuedInvoices counts erp/billing's issued invoices by legal entity: each forms one receivable here.
func issuedInvoices(t string, les map[string]*entity) map[string]int {
	billing := client{seed.NewClient(env("ERP_BILLING_URL", "http://localhost:18089"), t)}
	out := map[string]int{}
	for _, code := range entities {
		out[code] = len(billing.pages("/api/billing/invoices?state=issued&legal_entity_id="+les[code].id, "invoices"))
	}
	return out
}

// done reports whether the legal entity's documents were seeded: the last step reopens a period.
func done(c client, e *entity) bool {
	for _, p := range c.must("GET", "/api/finance/periods?legal_entity_id="+e.id, nil)["periods"].([]any) {
		if p.(map[string]any)["state"] == "reopened" {
			return true
		}
	}
	return false
}
