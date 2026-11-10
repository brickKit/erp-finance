package intake

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// accountingFact is what the posting reads of an accounting fact (contract/events/shared/accounting_fact.v1).
type accountingFact struct {
	FactID string `json:"fact_id"`
	Source struct {
		DocumentType   string  `json:"document_type"`
		DocumentNumber *string `json:"document_number"`
	} `json:"source"`
	BusinessDate string            `json:"business_date"`
	PostingDate  string            `json:"posting_date"`
	RuleKey      string            `json:"rule_key"`
	Currency     string            `json:"currency"`
	Amounts      map[string]string `json:"amounts"`
	PartnerID    *string           `json:"partner_id"`
	ExchangeRate *struct {
		Rate string `json:"rate"`
	} `json:"exchange_rate"`
	Dimensions struct {
		DepartmentID *string `json:"department_id"`
	} `json:"dimensions"`
}

// postAccounting posts an accounting fact by its rule and the legal entity's mapping (AF01, AF02), in the posting
// transaction: a rule or a role not mapped waits (MAPPING_MISSING, from ledger.Resolve); a fact that cannot balance
// or is malformed stops its item (work.Permanent) and stays pending for a person.
func postAccounting(m *core.Base, ctx context.Context, tx db.Tx, f core.Received) (string, error) {
	var af accountingFact
	if err := json.Unmarshal(f.Data, &af); err != nil || len(af.Amounts) == 0 ||
		!core.DateForm.MatchString(af.BusinessDate) || !core.DateForm.MatchString(af.PostingDate) {
		return "", stop(m, f, errors.New("the accounting fact is malformed"))
	}
	for role, a := range af.Amounts {
		if !core.DecimalForm.MatchString(a) {
			return "", stop(m, f, errors.New("the amount of "+role+" is not a decimal"))
		}
	}
	var rate *string
	if af.Currency != f.BaseCurrency {
		if af.ExchangeRate == nil {
			return "", stop(m, f, errors.New("an accounting fact in "+af.Currency+" has no rate"))
		}
		rate = &af.ExchangeRate.Rate
	}
	lines, err := ledger.Resolve(ctx, tx, f.LegalEntityID, af.RuleKey, af.Amounts)
	if err != nil {
		return "", err
	}
	for i := range lines {
		lines[i].PartnerID, lines[i].DepartmentID = af.PartnerID, af.Dimensions.DepartmentID
	}
	date, err := periods.PostingDate(m, ctx, tx, f.LegalEntityID, af.PostingDate)
	if err != nil {
		return "", err
	}
	description := af.Source.DocumentType
	if af.Source.DocumentNumber != nil {
		description += " " + *af.Source.DocumentNumber
	}
	id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: f.LegalEntityID, BusinessDate: af.BusinessDate,
		PostingDate: date, Description: description, Source: ledger.Source{Kind: "accounting_fact", ID: &af.FactID},
		State: "posted", Lines: ledger.Convert(lines, af.Currency, rate, f.BaseScale)})
	if errs.Is(err, core.ID, "VOUCHER_UNBALANCED") || errs.Is(err, core.ID, "ACCOUNT_NOT_POSTABLE") {
		return "", stop(m, f, err)
	}
	if err != nil {
		return "", err
	}
	_, err = ledger.Issue(m, ctx, tx, id, f.LegalEntityCode, f.Source, core.OpPostFact, nil)
	return id, err
}

// stop gives up posting a fact a person must look at: logged, its item stopped, the fact left pending.
func stop(m *core.Base, f core.Received, err error) error {
	m.RT.Logger.Error("a fact cannot be posted; it waits for a person", "event", "fact_not_postable", "fact_id",
		f.FactID, "type", f.Type, "source", f.Source, "error", err.Error())
	return work.Permanent(err)
}
