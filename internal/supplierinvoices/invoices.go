package supplierinvoices

import (
	"context"
	"errors"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	orgunits "github.com/brickKit/erp-finance/v3/internal/orgunits"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"
	"github.com/jackc/pgx/v5/pgconn"
)

type lineIn struct {
	OrderLineID        *string `json:"order_line_id"`
	Description        *string `json:"description"`
	ExpenseAccountCode *string `json:"expense_account_code"`
	Quantity           string  `json:"quantity"`
	UnitPrice          string  `json:"unit_price"`
	Tax                string  `json:"tax"`
}

type invoiceIn struct {
	LegalEntityID  string   `json:"legal_entity_id"`
	SupplierID     string   `json:"supplier_id"`
	SupplierNumber string   `json:"supplier_number"`
	InvoiceDate    string   `json:"invoice_date"`
	DueDate        string   `json:"due_date"`
	Currency       string   `json:"currency"`
	DepartmentID   *string  `json:"department_id"`
	Version        *int64   `json:"version"`
	Lines          []lineIn `json:"lines"`
}

// prepared is an invoice's input checked, with its evidence (SI01): the legal entity, its kind, the department's path,
// the lines' amounts.
type prepared struct {
	le             masterdata.LegalEntity
	kind           string
	departmentID   *string
	departmentPath *string
	lines          []Line
}

func prepare(m *core.Base, ctx context.Context, in *invoiceIn) (prepared, error) {
	var p prepared
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return p, core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case !core.UUIDForm.MatchString(in.SupplierID):
		return p, core.Invalid("supplier_id", "supplier_id is a partner's ID")
	case strings.TrimSpace(in.SupplierNumber) == "":
		return p, core.Invalid("supplier_number", "supplier_number is required")
	case !core.DateForm.MatchString(in.InvoiceDate) || !core.DateForm.MatchString(in.DueDate):
		return p, core.Invalid("invoice_date", "invoice_date and due_date are YYYY-MM-DD")
	case len(in.Lines) == 0 || len(in.Lines) > 200:
		return p, core.Invalid("lines", "an invoice has 1-200 lines")
	}
	orders := 0
	for i, l := range in.Lines {
		field := fmt.Sprintf("lines[%d]", i)
		switch {
		case l.OrderLineID != nil && !core.UUIDForm.MatchString(*l.OrderLineID):
			return p, core.Invalid(field+".order_line_id", "order_line_id is an order line's ID")
		case l.OrderLineID != nil && l.ExpenseAccountCode != nil:
			return p, core.Invalid(field, "a line is an order line's or an expense account's, not both")
		case l.OrderLineID == nil && (l.ExpenseAccountCode == nil || *l.ExpenseAccountCode == ""):
			return p, core.Invalid(field+".expense_account_code", "an expense line names its account")
		}
		if l.OrderLineID != nil {
			orders++
		}
	}
	switch orders {
	case len(in.Lines):
		p.kind = "order"
		if !masterdata.PurchaseDeployed(m) {
			return p, errs.New(errs.ReasonCapabilityUnsupported, "erp/purchase is not deployed: an invoice against orders cannot be registered")
		}
	case 0:
		p.kind = "expense"
	default:
		return p, core.Invalid("lines", "an invoice's lines are all order lines or all expense lines")
	}
	var err error
	if p.le, err = masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return p, err
	}
	if in.Currency != p.le.BaseCurrency {
		return p, core.ErrCurrency.New("a supplier invoice is in the legal entity's currency " + p.le.BaseCurrency + " (foreign currency later)")
	}
	scale, err := masterdata.CurrencyScale(m, ctx, in.Currency)
	if err != nil {
		return p, err
	}
	if _, err := masterdata.PartnerOf(m, ctx, in.SupplierID); err != nil {
		return p, err
	}
	if p.departmentID, p.departmentPath, err = orgunits.Usable(m, ctx, in.DepartmentID, in.LegalEntityID); err != nil {
		return p, err
	}
	for i, l := range in.Lines {
		field := fmt.Sprintf("lines[%d]", i)
		qty, err := core.Decimal(field+".quantity", l.Quantity, core.PriceScale, true)
		if err != nil {
			return p, err
		}
		price, err := core.Decimal(field+".unit_price", l.UnitPrice, core.PriceScale, false)
		if err != nil {
			return p, err
		}
		tax, err := core.Decimal(field+".tax", l.Tax, scale, false)
		if err != nil {
			return p, err
		}
		p.lines = append(p.lines, Line{ID: ids.New().String(), OrderLineID: l.OrderLineID, Description: l.Description,
			ExpenseAccountCode: l.ExpenseAccountCode, Quantity: qty, UnitPrice: price, Net: core.Times(qty, price, scale), Tax: tax})
	}
	return p, nil
}

// writeLines replaces the invoice's lines; an expense account must be postable.
func writeLines(ctx context.Context, tx db.Tx, v invoiceIn, id string, p prepared) error {
	if _, err := tx.Exec(ctx, `DELETE FROM supplier_invoice_line WHERE invoice_id = $1`, id); err != nil {
		return err
	}
	for i, l := range p.lines {
		if l.ExpenseAccountCode != nil {
			if err := ledger.Postable(ctx, tx, v.LegalEntityID, *l.ExpenseAccountCode); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_invoice_line (id, invoice_id, line_no, order_line_id,
			expense_account_code, description, quantity, unit_price, net, tax) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			l.ID, id, i+1, l.OrderLineID, l.ExpenseAccountCode, l.Description, l.Quantity, l.UnitPrice, l.Net, l.Tax); err != nil {
			return err
		}
	}
	return nil
}

// taken turns the supplier number's unique index into CODE_TAKEN.
func taken(err error, in invoiceIn) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "supplier_invoice_supplier_number" {
		return core.ErrCode.New("the supplier's invoice " + in.SupplierNumber + " is registered")
	}
	return err
}

// CreateSupplierInvoice registers an invoice in draft (SI01); its maker is the one who registers it.
func CreateSupplierInvoice(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in invoiceIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Version != nil {
		return core.Invalid("version", "a new invoice has no version")
	}
	p, err := prepare(m, c.Ctx(), &in)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpSupplierInvEdit, core.DocFacts(in.LegalEntityID, p.departmentPath))
	if err != nil {
		return err
	}
	number, err := core.Number(m, ctx, "SI", in.LegalEntityID, p.le.Code, in.InvoiceDate)
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		id := ids.New().String()
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_invoice (id, number, legal_entity_id, supplier_id, supplier_number,
			invoice_date, due_date, currency, kind, maker_id, department_id, department_path, search, search_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`, id, number, in.LegalEntityID, in.SupplierID,
			in.SupplierNumber, in.InvoiceDate, in.DueDate, in.Currency, p.kind, actor, p.departmentID, p.departmentPath,
			search.Text(number, in.SupplierNumber), search.Version); err != nil {
			return idem.Outcome{}, taken(err, in)
		}
		if err := writeLines(ctx, tx, in, id, p); err != nil {
			return idem.Outcome{}, err
		}
		v, err := Get(ctx, tx, id, false)
		if err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(id), core.Change(m, ctx, tx, actor, core.OpSupplierInvEdit, core.SupplierInvoices, id, v)
	})
	if err != nil {
		return err
	}
	v, err := Read(m, ctx, core.ResultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, v)
}

func GetSupplierInvoice(m *core.Base, c *httpx.Context) error {
	v, err := Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpSupplierInvRead, core.DocFacts(v.LegalEntityID, v.DepartmentPath)); err != nil {
		return err
	}
	return c.JSON(200, v)
}

// UpdateSupplierInvoice changes an invoice before posting, its whole lines, by version (SI01): back to draft, its
// matching and its approval cleared. The legal entity stays.
func UpdateSupplierInvoice(m *core.Base, c *httpx.Context) error {
	var in invoiceIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	cur, err := Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpSupplierInvEdit, core.DocFacts(cur.LegalEntityID, cur.DepartmentPath))
	if err != nil {
		return err
	}
	if in.LegalEntityID != cur.LegalEntityID {
		return core.Invalid("legal_entity_id", "an invoice's legal entity does not change")
	}
	if d := Lifecycle.Decide(cur.State, Edit); d.Effect == core.Refuse {
		return core.Refused(d, cur.State)
	}
	p, err := prepare(m, ctx, &in)
	if err != nil {
		return err
	}
	var out Invoice
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := Get(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		if d := Lifecycle.Decide(v.State, Edit); d.Effect == core.Refuse {
			return core.Refused(d, v.State)
		}
		if in.Version != nil && *in.Version != v.Version {
			return core.ErrVersion.New(fmt.Sprintf("the invoice is at version %d", v.Version))
		}
		if _, err := tx.Exec(ctx, `UPDATE supplier_invoice SET supplier_id = $2, supplier_number = $3, invoice_date = $4,
			due_date = $5, currency = $6, kind = $7, department_id = $8, department_path = $9, search = $10, search_version = $11,
			state = 'draft', difference_approved_by = NULL, difference_reason = NULL, version = version + 1, updated_at = now()
			WHERE id = $1`, v.ID, in.SupplierID, in.SupplierNumber, in.InvoiceDate, in.DueDate, in.Currency, p.kind,
			p.departmentID, p.departmentPath, search.Text(v.Number, in.SupplierNumber), search.Version); err != nil {
			return taken(err, in)
		}
		if err := writeLines(ctx, tx, in, v.ID, p); err != nil {
			return err
		}
		if out, err = Get(ctx, tx, v.ID, false); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpSupplierInvEdit, core.SupplierInvoices, v.ID, out)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

var listSpec = list.Spec{Operation: core.OpSupplierInvRead, Alias: "i", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "supplier_id", Column: "supplier_id", Kind: list.UUID, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number", Column: "search", Fields: []string{"number", "supplier_number"}}}}

// ListSupplierInvoices is one page, newest first, by legal entity, supplier, state and the internal or the supplier's
// number.
func ListSupplierInvoices(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, p := range []string{"legal_entity_id", "supplier_id", "state"} {
		v := c.Query(p)
		if v == "" {
			continue
		}
		if p != "state" && !core.UUIDForm.MatchString(v) {
			return core.Invalid(p, p+" is an ID")
		}
		f = append(f, list.Filter{Field: p, Op: "eq", Values: []string{v}})
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
	out, next, err := list.Page(ctx, m.RT.DB, q, "supplier_invoice", Cols, Scan,
		func(v Invoice) ([]string, string) { return []string{v.CreatedAt}, v.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"supplier_invoices": out, "next_cursor": next})
}
