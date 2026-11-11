package main

import (
	"fmt"
	"time"
)

// seeder seeds one legal entity's documents: maker makes them, lead approves (never the same account), seed reads.
type seeder struct {
	e                 *entity
	maker, lead, seed client
}

func (s *seeder) key(kind string, n int) string {
	return fmt.Sprintf("seed-finance-%s-%s-%d", kind, s.e.code, n)
}

// act sends an action on a document as c and wants 200.
func (s *seeder) act(c client, path string, body any, header ...string) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	return c.must("POST", path, body, header...)
}

// voucher templates: the debit account, the credit account, what it is, whether the debit line names a department.
var templates = []struct {
	debit, credit, what string
	dept                bool
}{
	{"6602", "1002", "办公用品", true}, {"6601", "2241", "市场推广费（应付）", true}, {"1221", "1002", "员工备用金", false},
	{"6603", "1002", "银行手续费", false}, {"6602", "2211", "计提管理人员工资", true}, {"1601", "1002", "购置办公设备", false},
	{"1002", "6051", "其他业务收入（租金）", false}, {"6601", "1002", "差旅费", true},
}

// counts of manual vouchers by legal entity: posted ones in each of the three months, and the other states (now).
var posted = map[string]int{"CN01": 50, "ES01": 8, "US01": 6}

// monthDay is the date n months from now (in the month, day d, not after today in the current month).
func monthDay(n, d int) string {
	now := time.Now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	if n == 0 && d > now.Day() {
		d = now.Day()
	}
	if d > 28 {
		d = 28
	}
	return first.AddDate(0, 0, d-1).Format("2006-01-02")
}

func (s *seeder) voucherBody(i int, date string) map[string]any {
	t := templates[i%len(templates)]
	amount := fmt.Sprintf("%d.%02d", 200+(i*137)%9800, (i*29)%100)
	debit := map[string]any{"account_code": t.debit, "side": "debit", "amount": amount}
	if t.dept {
		debit["department_id"] = s.e.depts[i%len(s.e.depts)]
	}
	return map[string]any{"legal_entity_id": s.e.id, "business_date": date,
		"description": fmt.Sprintf("%s%s %d", prefix, t.what, i+1),
		"lines":       []any{debit, map[string]any{"account_code": t.credit, "side": "credit", "amount": amount}}}
}

// voucher makes voucher i as the maker and moves it on to want (draft, submitted, rejected, posted), from the state it
// is in.
func (s *seeder) voucher(i int, date, want string) map[string]any {
	v := s.maker.must("POST", "/api/finance/vouchers", s.voucherBody(i, date), "Idempotency-Key", s.key("voucher", i))
	path := "/api/finance/vouchers/" + v["id"].(string)
	v = s.seed.must("GET", path, nil) // a repeated key answers what the first request made: read the state now
	if want == "draft" || v["state"] == "posted" || v["state"] == "reversed" {
		return v
	}
	if v["state"] == "draft" && (want != "rejected" || v["reason"] == nil) {
		v = s.act(s.maker, path+"/submit", nil)
	}
	switch {
	case want == "rejected" && v["state"] == "submitted":
		v = s.act(s.lead, path+"/reject", map[string]any{"reason": prefix + "附件不全，请补充后重新提交"})
	case want == "posted" && v["state"] == "submitted":
		v = s.act(s.lead, path+"/approve", nil)
	}
	return v
}

// vouchers seeds the manual vouchers: posted ones two months ago, last month and this month, then the other states
// this month; the first two posted this month are reversed.
func (s *seeder) vouchers() int {
	n, i := posted[s.e.code], 0
	for _, month := range []int{-2, -1, 0} {
		for k := 0; k < n; k++ {
			v := s.voucher(i, monthDay(month, 1+k%28), "posted")
			if month == 0 && k < 2 && v["state"] == "posted" {
				s.act(s.lead, "/api/finance/vouchers/"+v["id"].(string)+"/reverse",
					map[string]any{"reason": prefix + "金额录错，冲销后重做"}, "Idempotency-Key", s.key("reverse", i))
			}
			i++
		}
	}
	for _, want := range []string{"draft", "draft", "draft", "submitted", "submitted", "submitted", "rejected", "rejected"} {
		s.voucher(i, monthDay(0, 1+i%28), want)
		i++
	}
	return i
}

// periods closes the month two months ago, then last month, and reopens last month with the reason (the last step:
// done() looks for it).
func (s *seeder) periods() {
	state := func(month string) string {
		for _, p := range s.seed.must("GET", "/api/finance/periods?legal_entity_id="+s.e.id, nil)["periods"].([]any) {
			if p := p.(map[string]any); p["period"] == month {
				return p["state"].(string)
			}
		}
		fail("%s has no period %s", s.e.code, month)
		return ""
	}
	path := func(month, action string) string {
		return "/api/finance/periods/" + s.e.id + "/" + month + "/" + action
	}
	twoAgo, lastMonth := monthDay(-2, 1)[:7], monthDay(-1, 1)[:7]
	if state(twoAgo) == "open" {
		s.act(s.lead, path(twoAgo, "close"), nil)
	}
	if state(lastMonth) == "open" {
		s.act(s.lead, path(lastMonth, "close"), nil)
	}
	if state(lastMonth) == "closed" {
		s.act(s.lead, path(lastMonth, "reopen"), map[string]any{"reason": prefix + "补录上月漏记的费用"})
	}
}
