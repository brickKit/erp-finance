package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	claims "github.com/brickKit/erp-finance/v3/internal/claims"
	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// fundFact is what the settlement reads of a fund fact (contract/events/shared/fund_fact.v1).
type fundFact struct {
	FactID string `json:"fact_id"`
	Source struct {
		DocumentNumber *string `json:"document_number"`
	} `json:"source"`
	BusinessDate string  `json:"business_date"`
	PostingDate  string  `json:"posting_date"`
	RuleKey      string  `json:"rule_key"`
	Currency     string  `json:"currency"`
	Kind         string  `json:"kind"`
	PartnerID    *string `json:"partner_id"`
	Amount       string  `json:"amount"`
	ExchangeRate *struct {
		Rate string `json:"rate"`
	} `json:"exchange_rate"`
	Dimensions struct {
		DepartmentID *string `json:"department_id"`
	} `json:"dimensions"`
	Claims []struct {
		ClaimID string `json:"claim_id"`
		Amount  string `json:"amount"`
	} `json:"claims"`
	ReversesFactID *string `json:"reverses_fact_id"`
}

// PostFund posts a fund fact and settles it in the posting transaction (ST02-ST05) and answers its voucher.
func PostFund(m *core.Base, ctx context.Context, tx db.Tx, f core.Received) (string, error) {
	var ff fundFact
	if err := json.Unmarshal(f.Data, &ff); err != nil || !core.DecimalForm.MatchString(ff.Amount) {
		return "", work.Permanent(errors.New("settlement: fund fact " + f.FactID + " is not readable"))
	}
	if ff.Kind == "reversal" {
		return reverse(m, ctx, tx, f, ff)
	}
	vid, err := fundVoucher(m, ctx, tx, f, ff)
	if err != nil {
		return "", err
	}
	rest := ff.Amount
	switch ff.Kind {
	case "receipt":
		if ff.PartnerID == nil {
			return vid, nil // suspense: nothing allocated (ST02)
		}
		if rest, err = receive(m, ctx, tx, f.LegalEntityID, ff); err != nil {
			return "", err
		}
		if core.Cmp(rest, "0") > 0 { // ST03: the rest is the customer's credit balance
			_, err = openitems.Form(m, ctx, tx, openitems.New{LegalEntityID: f.LegalEntityID, Kind: "credit_balance",
				PartnerID: *ff.PartnerID, Currency: ff.Currency, Amount: rest, DueDate: ff.BusinessDate, Origin: "fund",
				Source: openitems.Source{Kind: "fund_fact", ID: &ff.FactID, Number: ff.Source.DocumentNumber}, VoucherID: vid})
		}
	case "payment", "refund":
		if rest, err = pay(m, ctx, tx, f, ff); err != nil {
			return "", err
		}
		if core.Cmp(rest, "0") > 0 {
			m.RT.Logger.Warn("a payment's money is not all settled", "event", "fund_unallocated", "fact_id", ff.FactID,
				"unallocated", rest)
		}
	default:
		return "", work.Permanent(errors.New("settlement: fund fact " + f.FactID + " of kind " + ff.Kind))
	}
	return vid, err
}

// fundVoucher posts the fund fact by its kind's roles (ST02).
func fundVoucher(m *core.Base, ctx context.Context, tx db.Tx, f core.Received, ff fundFact) (string, error) {
	var amounts map[string]string
	switch ff.Kind {
	case "receipt":
		amounts = map[string]string{"bank": ff.Amount, "receivable": ff.Amount}
		if ff.PartnerID == nil {
			amounts = map[string]string{"bank": ff.Amount, "suspense": ff.Amount}
		}
	case "payment":
		amounts = map[string]string{"payable": ff.Amount, "bank": ff.Amount}
	case "refund":
		amounts = map[string]string{"receivable": ff.Amount, "bank": ff.Amount}
	default:
		return "", work.Permanent(errors.New("settlement: fund fact " + f.FactID + " of kind " + ff.Kind))
	}
	var rate *string
	if ff.Currency != f.BaseCurrency {
		if ff.ExchangeRate == nil {
			return "", work.Permanent(errors.New("settlement: fund fact " + f.FactID + " in " + ff.Currency + " has no rate"))
		}
		rate = &ff.ExchangeRate.Rate
	}
	lines, err := ledger.Resolve(ctx, tx, f.LegalEntityID, ff.RuleKey, amounts)
	if err != nil {
		return "", err
	}
	for i := range lines {
		lines[i].PartnerID, lines[i].DepartmentID = ff.PartnerID, ff.Dimensions.DepartmentID
	}
	date, err := periods.PostingDate(m, ctx, tx, f.LegalEntityID, ff.PostingDate)
	if err != nil {
		return "", err
	}
	id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: f.LegalEntityID, BusinessDate: ff.BusinessDate,
		PostingDate: date, Description: "资金 " + ff.Kind + " " + deref(ff.Source.DocumentNumber),
		Source: ledger.Source{Kind: "fund_fact", ID: &ff.FactID}, State: "posted",
		Lines: ledger.Convert(lines, ff.Currency, rate, f.BaseScale)})
	if err != nil {
		return "", err
	}
	_, err = ledger.Issue(m, ctx, tx, id, f.LegalEntityCode, f.Source, core.OpPostFact, nil)
	return id, err
}

// receive allocates a receipt to the customer's receivables of the legal entity in its currency, the earliest due first (ST03), each
// up to its available balance, and answers what is left.
func receive(m *core.Base, ctx context.Context, tx db.Tx, le string, ff fundFact) (string, error) {
	var ids []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM open_item WHERE legal_entity_id = $1 AND partner_id = $2
		AND kind = 'receivable' AND state = 'open' AND currency = $3 AND amount - settled - pay_claimed > 0`,
		le, *ff.PartnerID, ff.Currency)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	items, err := lockItems(ctx, tx, ids)
	if err != nil {
		return "", err
	}
	order := make([]openitems.OpenItem, 0, len(items))
	for _, o := range items {
		order = append(order, o)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].DueDate != order[j].DueDate {
			return order[i].DueDate < order[j].DueDate
		}
		return order[i].ID < order[j].ID
	})
	rest := ff.Amount
	for _, o := range order {
		if core.Cmp(rest, "0") <= 0 {
			break
		}
		take := o.Available // read under the lock
		if core.Cmp(rest, take) < 0 {
			take = rest
		}
		if core.Cmp(take, "0") <= 0 {
			continue
		}
		if err := allocate(m, ctx, tx, allocation{fund: ff.FactID, item: o, amount: take, direction: "settle"}); err != nil {
			return "", err
		}
		rest = core.Minus(rest, take)
	}
	return rest, nil
}

// pay settles a payment or a refund by its claims (ST03, CL07): each confirmed claim of the partner in the fund's
// currency is formalized at the claim list's amount and allocated; another is left unallocated with a warning.
func pay(m *core.Base, ctx context.Context, tx db.Tx, f core.Received, ff fundFact) (string, error) {
	sort.Slice(ff.Claims, func(i, j int) bool { return ff.Claims[i].ClaimID < ff.Claims[j].ClaimID })
	records := make([]claims.Record, len(ff.Claims)) // the claims locked first, then their open items (as claims does)
	var items []string
	for i, c := range ff.Claims {
		r, err := claims.Get(ctx, tx, c.ClaimID)
		if err != nil {
			return "", err
		}
		records[i] = r
		if r.State != claims.None {
			items = append(items, r.OpenItemID)
		}
	}
	locked, err := lockItems(ctx, tx, items)
	if err != nil {
		return "", err
	}
	rest := ff.Amount
	for i, c := range ff.Claims {
		r := records[i]
		o, ok := locked[r.OpenItemID]
		usable := r.State == claims.Confirmed && ok && ff.PartnerID != nil && o.PartnerID == *ff.PartnerID &&
			o.Currency == ff.Currency && core.DecimalForm.MatchString(c.Amount)
		amount := "0"
		if usable {
			amount = c.Amount
			if core.Cmp(amount, r.Amount) > 0 {
				amount = r.Amount
			}
			if core.Cmp(amount, rest) > 0 {
				amount = rest
			}
		}
		if !usable || core.Cmp(amount, "0") <= 0 {
			m.RT.Logger.Warn("a fund fact's claim is not settled", "event", "claim_not_settled", "fact_id", ff.FactID,
				"claim_id", c.ClaimID, "state", r.State)
			continue
		}
		if _, err := claims.Formalize(m, ctx, tx, r, amount, f.Source); err != nil {
			return "", err
		}
		o, err = openitems.Get(ctx, tx, o.ID, false)
		if err != nil {
			return "", err
		}
		claim := r.ClaimID
		if err := allocate(m, ctx, tx, allocation{fund: ff.FactID, item: o, amount: amount, direction: "settle",
			claim: &claim}); err != nil {
			return "", err
		}
		rest = core.Minus(rest, amount)
	}
	return rest, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
