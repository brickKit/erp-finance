package ledger

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

// Source is what a voucher was posted from: a fact's rule key and ID, or manual.
type Source struct {
	Kind string  `json:"kind"`
	ID   *string `json:"id"`
}

// Line is a voucher line.
type Line struct {
	AccountCode  string  `json:"account_code"`
	Side         string  `json:"side"`
	Currency     string  `json:"currency"`
	Amount       string  `json:"amount"`
	BaseAmount   string  `json:"base_amount"`
	PartnerID    *string `json:"partner_id"`
	DepartmentID *string `json:"department_id"`
}

// Voucher is a voucher as the user surface reads it.
type Voucher struct {
	ID                string  `json:"id"`
	LegalEntityID     string  `json:"legal_entity_id"`
	Number            *string `json:"number"`
	Period            string  `json:"period"`
	BusinessDate      string  `json:"business_date"`
	PostingDate       string  `json:"posting_date"`
	Description       string  `json:"description"`
	Source            Source  `json:"source"`
	State             string  `json:"state"`
	MakerID           *string `json:"maker_id"`
	ApproverID        *string `json:"approver_id"`
	Reason            *string `json:"reason"`
	ReversesVoucherID *string `json:"reverses_voucher_id"`
	ReversedByID      *string `json:"reversed_by_id"`
	Lines             []Line  `json:"lines"`
	Version           int64   `json:"version"`
	UpdatedAt         string  `json:"updated_at"`

	CreatedAt string `json:"-"`
}

// Cols reads a voucher (alias v) with its lines.
const Cols = `v.id::text, v.legal_entity_id::text, v.number, v.period, v.business_date::text, v.posting_date::text,
	v.description, v.source_kind, v.source_id, v.state, v.maker_id, v.approver_id, v.reason, v.reverses_voucher_id::text,
	v.reversed_by_id::text, v.version, v.created_at, v.updated_at,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('account_code', l.account_code, 'side', l.side, 'currency', l.currency,
		'amount', trim_scale(l.amount)::text, 'base_amount', trim_scale(l.base_amount)::text,
		'partner_id', l.partner_id::text, 'department_id', l.department_id::text) ORDER BY l.line_no)
		FROM voucher_line l WHERE l.voucher_id = v.id), '[]'::jsonb)`

// Scan reads one row of Cols.
func Scan(row pgx.Row) (Voucher, error) {
	var v Voucher
	var created, updated time.Time
	var lines []byte
	err := row.Scan(&v.ID, &v.LegalEntityID, &v.Number, &v.Period, &v.BusinessDate, &v.PostingDate, &v.Description,
		&v.Source.Kind, &v.Source.ID, &v.State, &v.MakerID, &v.ApproverID, &v.Reason, &v.ReversesVoucherID, &v.ReversedByID,
		&v.Version, &created, &updated, &lines)
	if err != nil {
		return v, err
	}
	v.CreatedAt, v.UpdatedAt = canon.Time(created), canon.Time(updated)
	return v, json.Unmarshal(lines, &v.Lines)
}

// Get reads a voucher in tx; lock takes its row's lock.
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Voucher, error) {
	if lock {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM voucher WHERE id = $1::uuid FOR UPDATE`, id); err != nil {
			return Voucher{}, err
		}
	}
	v, err := Scan(tx.QueryRow(ctx, `SELECT `+Cols+` FROM voucher v WHERE v.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, core.NotFound("voucher")
	}
	return v, err
}

// Read reads a voucher in its own transaction; an ID of the wrong form is not found.
func Read(m *core.Base, ctx context.Context, id string) (Voucher, error) {
	var v Voucher
	if !core.UUIDForm.MatchString(id) {
		return v, core.NotFound("voucher")
	}
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		v, err = Get(ctx, tx, id, false)
		return err
	})
	return v, err
}
