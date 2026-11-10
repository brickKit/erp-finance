package intake

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// invoice is what the posting reads of erp.invoice.issued.v1 (erp/billing's contract).
type invoice struct {
	FactID        string  `json:"fact_id"`
	Number        string  `json:"number"`
	LegalEntityID string  `json:"legal_entity_id"`
	CustomerID    string  `json:"customer_id"`
	Currency      string  `json:"currency"`
	OrderID       *string `json:"order_id"`
	InvoiceDate   string  `json:"invoice_date"`
	// DueDate: the invoice's own when its contract carries one (later, from the payment terms); its date otherwise
	DueDate      *string `json:"due_date"`
	ExchangeRate *struct {
		Rate string `json:"rate"`
	} `json:"exchange_rate"`
	Totals struct {
		Net   string `json:"net"`
		Tax   string `json:"tax"`
		Gross string `json:"gross"`
	} `json:"totals"`
}

// postInvoice forms a receivable from an issued invoice (AR01, protocol 05 step 2): in one transaction the voucher by
// the rule sales_invoice (receivable = gross, revenue = net, output tax = tax), the open item and its
// receivable.formed fact, and the fact marked posted. Evidence first: the legal entity (base currency, code) and the
// base currency's places.
func postInvoice(m *core.Base, ctx context.Context, r *work.Run, f received) error {
	var inv invoice
	if err := json.Unmarshal(f.Data, &inv); err != nil {
		return work.Permanent(err)
	}
	le, err := masterdata.LegalEntityOf(m, ctx, inv.LegalEntityID)
	if err != nil {
		return err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, le.BaseCurrency)
	if err != nil {
		return err
	}
	var rate *string
	if inv.Currency != le.BaseCurrency {
		if inv.ExchangeRate == nil {
			return work.Permanent(errors.New("intake: invoice " + inv.FactID + " in " + inv.Currency + " has no rate"))
		}
		rate = &inv.ExchangeRate.Rate
	}
	due := inv.InvoiceDate
	if inv.DueDate != nil {
		due = *inv.DueDate
	}
	return r.Complete(ctx, func(ctx context.Context, tx db.Tx) error {
		if pending, err := claim(ctx, tx, f.FactID); err != nil || !pending {
			return err
		}
		lines, err := ledger.Resolve(ctx, tx, le.ID, "sales_invoice",
			map[string]string{"receivable": inv.Totals.Gross, "revenue": inv.Totals.Net, "output_tax": inv.Totals.Tax})
		if err != nil {
			return err
		}
		for i := range lines {
			lines[i].PartnerID = &inv.CustomerID
		}
		date, err := periods.PostingDate(m, ctx, tx, le.ID, inv.InvoiceDate)
		if err != nil {
			return err
		}
		id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: le.ID, BusinessDate: inv.InvoiceDate, PostingDate: date,
			Description: "销售发票 " + inv.Number, Source: ledger.Source{Kind: "sales_invoice", ID: &inv.FactID},
			State: "posted", Lines: ledger.Convert(lines, inv.Currency, rate, scale)})
		if err != nil {
			return err
		}
		if _, err := ledger.Issue(m, ctx, tx, id, le.Code, f.Source, core.OpPostFact, nil); err != nil {
			return err
		}
		if _, err := openitems.Form(m, ctx, tx, openitems.New{LegalEntityID: le.ID, Kind: "receivable",
			PartnerID: inv.CustomerID, Currency: inv.Currency, Amount: inv.Totals.Gross, DueDate: due, Origin: "invoice",
			Source: openitems.Source{Kind: "invoice", ID: &inv.FactID, Number: &inv.Number}, VoucherID: id,
			OrderID: inv.OrderID}); err != nil {
			return err
		}
		return posted(ctx, tx, f.FactID, id)
	})
}
