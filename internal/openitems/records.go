package openitems

import (
	"context"
	"errors"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/jackc/pgx/v5"
)

// Source is the document an open item came from: an invoice, a supplier invoice, a manual registration, a fund.
type Source struct {
	Kind   string  `json:"kind"`
	ID     *string `json:"id"`
	Number *string `json:"number"`
}

// Claim and Allocation are the claims on an open item and its settlements (filled by the claims and settlement
// modules; empty until they exist).
type Claim struct {
	ClaimID string `json:"claim_id"`
	Amount  string `json:"amount"`
	State   string `json:"state"`
}

type Allocation struct {
	ID         string `json:"id"`
	FundFactID string `json:"fund_fact_id"`
	Amount     string `json:"amount"`
	Direction  string `json:"direction"`
}

// OpenItem is an open item as the user surface reads it: available = amount - settled - pay_claimed, open_balance =
// amount - settled; its state is settled once the open balance is zero.
type OpenItem struct {
	ID            string       `json:"id"`
	LegalEntityID string       `json:"legal_entity_id"`
	Kind          string       `json:"kind"`
	PartnerID     string       `json:"partner_id"`
	Currency      string       `json:"currency"`
	Amount        string       `json:"amount"`
	Settled       string       `json:"settled"`
	PayClaimed    string       `json:"pay_claimed"`
	Available     string       `json:"available"`
	OpenBalance   string       `json:"open_balance"`
	DueDate       string       `json:"due_date"`
	Origin        string       `json:"origin"`
	Source        Source       `json:"source"`
	State         string       `json:"state"`
	DepartmentID  *string      `json:"department_id"`
	Claims        []Claim      `json:"claims"`
	Allocations   []Allocation `json:"allocations"`
	Version       int64        `json:"version"`
	UpdatedAt     string       `json:"updated_at"`

	DepartmentPath *string `json:"-"`
	CreatedAt      string  `json:"-"`
}

// Cols reads an open item (alias o).
const Cols = `o.id::text, o.legal_entity_id::text, o.kind, o.partner_id::text, o.currency, trim_scale(o.amount)::text,
	trim_scale(o.settled)::text, trim_scale(o.pay_claimed)::text, trim_scale(o.amount - o.settled - o.pay_claimed)::text,
	trim_scale(o.amount - o.settled)::text, o.due_date::text, o.origin, o.source_kind, o.source_id, o.source_number,
	CASE WHEN o.state = 'open' AND o.amount = o.settled THEN 'settled' ELSE o.state END, o.department_id::text,
	o.department_path, o.version, o.created_at, o.updated_at`

// Scan reads one row of Cols.
func Scan(row pgx.Row) (OpenItem, error) {
	o := OpenItem{Claims: []Claim{}, Allocations: []Allocation{}}
	var created, updated time.Time
	err := row.Scan(&o.ID, &o.LegalEntityID, &o.Kind, &o.PartnerID, &o.Currency, &o.Amount, &o.Settled, &o.PayClaimed,
		&o.Available, &o.OpenBalance, &o.DueDate, &o.Origin, &o.Source.Kind, &o.Source.ID, &o.Source.Number, &o.State,
		&o.DepartmentID, &o.DepartmentPath, &o.Version, &created, &updated)
	o.CreatedAt, o.UpdatedAt = canon.Time(created), canon.Time(updated)
	return o, err
}

// Get reads an open item in tx; lock takes its row's lock.
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (OpenItem, error) {
	q := `SELECT ` + Cols + ` FROM open_item o WHERE o.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	o, err := Scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, core.NotFound("open item")
	}
	return o, err
}

// Read reads an open item in its own transaction; an ID of the wrong form is not found.
func Read(m *core.Base, ctx context.Context, id string) (OpenItem, error) {
	var o OpenItem
	if !core.UUIDForm.MatchString(id) {
		return o, core.NotFound("open item")
	}
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		o, err = Get(ctx, tx, id, false)
		return err
	})
	return o, err
}
