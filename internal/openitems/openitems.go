package openitems

import (
	"context"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"

	"time"
)

// New is an open item to form: from an issued invoice (AR01), later a supplier invoice or a manual registration.
type New struct {
	LegalEntityID  string
	Kind           string
	PartnerID      string
	Currency       string
	Amount         string
	DueDate        string
	Origin         string
	Source         Source
	VoucherID      string
	DepartmentID   *string
	DepartmentPath *string
	// OrderID is the sales order an invoice's receivable belongs to (erp/credit's branch), carried by the fact
	OrderID *string
}

// Form makes an open item in tx and, for a receivable, publishes erp.receivable.formed.v1 (protocol 05 step 2;
// the fact's ID is the open item's).
func Form(m *core.Base, ctx context.Context, tx db.Tx, n New) (OpenItem, error) {
	id := ids.New().String()
	number := ""
	if n.Source.Number != nil {
		number = *n.Source.Number
	}
	if _, err := tx.Exec(ctx, `INSERT INTO open_item (id, legal_entity_id, kind, partner_id, currency, amount, due_date,
		origin, source_kind, source_id, source_number, voucher_id, search, search_version, department_id, department_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`, id, n.LegalEntityID, n.Kind,
		n.PartnerID, n.Currency, n.Amount, n.DueDate, n.Origin, n.Source.Kind, n.Source.ID, n.Source.Number, n.VoucherID,
		search.Text(number), search.Version, n.DepartmentID, n.DepartmentPath); err != nil {
		return OpenItem{}, err
	}
	o, err := Get(ctx, tx, id, false)
	if err != nil || n.Kind != "receivable" {
		return o, err
	}
	return o, core.Fact(m, ctx, tx, core.ReceivableFormed, id, n.LegalEntityID, map[string]any{
		"fact_id": id, "legal_entity_id": n.LegalEntityID, "customer_id": n.PartnerID, "invoice_id": n.Source.ID,
		"invoice_number": n.Source.Number, "order_id": n.OrderID, "currency": n.Currency, "amount": o.Amount,
		"due_date": n.DueDate, "recorded_at": canon.Time(time.Now())})
}

func GetOpenItem(m *core.Base, c *httpx.Context) error {
	o, err := Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpOpenItemRead, core.DocFacts(o.LegalEntityID, o.DepartmentPath)); err != nil {
		return err
	}
	return c.JSON(200, o)
}

var listSpec = list.Spec{Operation: core.OpOpenItemRead, Alias: "o", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "kind", Column: "kind", Kind: list.Text, Filterable: true},
		{Field: "partner_id", Column: "partner_id", Kind: list.UUID, Filterable: true},
		{Field: "open", Column: "is_open", Kind: list.Boolean, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number", Column: "search", Fields: []string{"source"}}}}

// ListOpenItems is one page, newest first, by legal entity, kind, partner and the source's number; open=true keeps
// those with an open balance.
func ListOpenItems(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, p := range []string{"legal_entity_id", "kind", "partner_id"} {
		v := c.Query(p)
		if v == "" {
			continue
		}
		if p != "kind" && !core.UUIDForm.MatchString(v) {
			return core.Invalid(p, p+" is an ID")
		}
		f = append(f, list.Filter{Field: p, Op: "eq", Values: []string{v}})
	}
	switch open := c.Query("open"); open {
	case "":
	case "true", "false":
		f = append(f, list.Filter{Field: "open", Op: "eq", Values: []string{open}})
	default:
		return core.Invalid("open", "open is true or false")
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, listSpec, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "open_item", Cols, Scan,
		func(o OpenItem) ([]string, string) { return []string{o.CreatedAt}, o.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"open_items": out, "next_cursor": next})
}
