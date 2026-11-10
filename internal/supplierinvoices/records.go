package supplierinvoices

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/jackc/pgx/v5"
)

// Result is a line's last matching (SI02).
type Result struct {
	Result              string   `json:"result"`
	InvoiceableQuantity string   `json:"invoiceable_quantity"`
	OrderUnitPrice      string   `json:"order_unit_price"`
	Problems            []string `json:"problems"`
}

// Line is an invoice line: an order line's (in its unit) or an expense account's.
type Line struct {
	ID                 string  `json:"id"`
	OrderLineID        *string `json:"order_line_id"`
	ExpenseAccountCode *string `json:"expense_account_code"`
	Description        *string `json:"description"`
	Quantity           string  `json:"quantity"`
	UnitPrice          string  `json:"unit_price"`
	Net                string  `json:"net"`
	Tax                string  `json:"tax"`
	Match              *Result `json:"match"`
}

// Invoice is a supplier invoice as the user surface reads it.
type Invoice struct {
	ID                   string  `json:"id"`
	Number               string  `json:"number"`
	LegalEntityID        string  `json:"legal_entity_id"`
	SupplierID           string  `json:"supplier_id"`
	SupplierNumber       string  `json:"supplier_number"`
	InvoiceDate          string  `json:"invoice_date"`
	DueDate              string  `json:"due_date"`
	Currency             string  `json:"currency"`
	Net                  string  `json:"net"`
	Tax                  string  `json:"tax"`
	Gross                string  `json:"gross"`
	State                string  `json:"state"`
	DifferenceApprovedBy *string `json:"difference_approved_by"`
	OpenItemID           *string `json:"open_item_id"`
	DepartmentID         *string `json:"department_id"`
	Lines                []Line  `json:"lines"`
	Version              int64   `json:"version"`
	UpdatedAt            string  `json:"updated_at"`

	Kind           string  `json:"-"`
	MakerID        string  `json:"-"`
	DepartmentPath *string `json:"-"`
	CreatedAt      string  `json:"-"`
}

// Cols reads an invoice (alias i) with its lines and totals.
const Cols = `i.id::text, i.number, i.legal_entity_id::text, i.supplier_id::text, i.supplier_number, i.invoice_date::text,
	i.due_date::text, i.currency,
	trim_scale(coalesce((SELECT sum(l.net) FROM supplier_invoice_line l WHERE l.invoice_id = i.id), 0))::text,
	trim_scale(coalesce((SELECT sum(l.tax) FROM supplier_invoice_line l WHERE l.invoice_id = i.id), 0))::text,
	trim_scale(coalesce((SELECT sum(l.net + l.tax) FROM supplier_invoice_line l WHERE l.invoice_id = i.id), 0))::text,
	i.state, i.difference_approved_by, i.open_item_id::text, i.department_id::text, i.version, i.updated_at, i.kind,
	i.maker_id, i.department_path, i.created_at,
	coalesce((SELECT jsonb_agg(jsonb_build_object('id', l.id, 'order_line_id', l.order_line_id,
		'expense_account_code', l.expense_account_code, 'description', l.description,
		'quantity', trim_scale(l.quantity)::text, 'unit_price', trim_scale(l.unit_price)::text,
		'net', trim_scale(l.net)::text, 'tax', trim_scale(l.tax)::text, 'match', l.match) ORDER BY l.line_no)
		FROM supplier_invoice_line l WHERE l.invoice_id = i.id), '[]'::jsonb)`

// Scan reads one row of Cols.
func Scan(row pgx.Row) (Invoice, error) {
	var v Invoice
	var updated, created time.Time
	var lines []byte
	err := row.Scan(&v.ID, &v.Number, &v.LegalEntityID, &v.SupplierID, &v.SupplierNumber, &v.InvoiceDate, &v.DueDate,
		&v.Currency, &v.Net, &v.Tax, &v.Gross, &v.State, &v.DifferenceApprovedBy, &v.OpenItemID, &v.DepartmentID,
		&v.Version, &updated, &v.Kind, &v.MakerID, &v.DepartmentPath, &created, &lines)
	if err != nil {
		return v, err
	}
	v.UpdatedAt, v.CreatedAt = canon.Time(updated), canon.Time(created)
	return v, json.Unmarshal(lines, &v.Lines)
}

// Get reads an invoice in tx; lock takes its row's lock.
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Invoice, error) {
	if lock {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM supplier_invoice WHERE id = $1::uuid FOR UPDATE`, id); err != nil {
			return Invoice{}, err
		}
	}
	v, err := Scan(tx.QueryRow(ctx, `SELECT `+Cols+` FROM supplier_invoice i WHERE i.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, core.NotFound("supplier invoice")
	}
	return v, err
}

// Read reads an invoice in its own transaction; an ID of the wrong form is not found.
func Read(m *core.Base, ctx context.Context, id string) (Invoice, error) {
	var v Invoice
	if !core.UUIDForm.MatchString(id) {
		return v, core.NotFound("supplier invoice")
	}
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		v, err = Get(ctx, tx, id, false)
		return err
	})
	return v, err
}

// orderLines are the invoice's order line IDs.
func (v Invoice) orderLines() []string {
	var out []string
	for _, l := range v.Lines {
		if l.OrderLineID != nil {
			out = append(out, *l.OrderLineID)
		}
	}
	return out
}
