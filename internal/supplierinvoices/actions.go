package supplierinvoices

import (
	"context"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
)

type actionIn struct {
	Version *int64  `json:"version"`
	Reason  *string `json:"reason"`
}

// evidence is what a command takes outside its transaction: the order lines' basis (match, post) and the legal
// entity with its currency's places (post).
type evidence struct {
	basis map[string]*purchasev3.BasisLine
	le    masterdata.LegalEntity
	scale int32
}

func gather(m *core.Base, ctx context.Context, v Invoice, command string) (evidence, error) {
	var e evidence
	var err error
	if command != Match && command != Post {
		return e, nil
	}
	if e.basis, err = masterdata.MatchBasis(m, ctx, v.orderLines()); err != nil || command != Post {
		return e, err
	}
	if e.le, err = masterdata.LegalEntityOf(m, ctx, v.LegalEntityID); err != nil {
		return e, err
	}
	e.scale, err = masterdata.CurrencyScale(m, ctx, v.Currency)
	return e, err
}

// Action is one of match, approve-difference, post and cancel, as the table decides it in the invoice's state; an
// ignored command answers the invoice as it is.
func Action(m *core.Base, command string) httpx.Handler {
	op := core.OpSupplierInvEdit
	if command == Approve {
		op = core.OpSupplierInvApprove
	}
	return func(c *httpx.Context) error {
		var in actionIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		cur, err := Read(m, c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, op, core.DocFacts(cur.LegalEntityID, cur.DepartmentPath))
		if err != nil {
			return err
		}
		if command == Approve && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "an approval says why")
		}
		if d := Lifecycle.Decide(cur.State, command); d.Effect == core.Refuse {
			return core.Refused(d, cur.State)
		}
		ev, err := gather(m, ctx, cur, command)
		if err != nil {
			return err
		}
		var out Invoice
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			v, err := Get(ctx, tx, cur.ID, true)
			if err != nil {
				return err
			}
			out = v
			d := Lifecycle.Decide(v.State, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, v.State)
			}
			if in.Version != nil && *in.Version != v.Version {
				return core.ErrVersion.New(fmt.Sprintf("the invoice is at version %d", v.Version))
			}
			if err := guard(d, v, actor); err != nil {
				return err
			}
			switch command {
			case Match:
				err = match(m, ctx, tx, v, ev)
			case Approve:
				_, err = tx.Exec(ctx, `UPDATE supplier_invoice SET difference_approved_by = $2, difference_reason = $3,
					version = version + 1, updated_at = now() WHERE id = $1`, v.ID, actor, *in.Reason)
			case Post:
				err = post(m, ctx, tx, v, ev, d, actor)
			case Cancel:
				err = setState(ctx, tx, v, Cancelled)
			}
			if err != nil {
				return err
			}
			if out, err = Get(ctx, tx, v.ID, false); err != nil {
				return err
			}
			if command == Match {
				return nil // a matching is the invoice's own evidence, not a business change
			}
			return core.Change(m, ctx, tx, actor, op, core.SupplierInvoices, v.ID, out)
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// guard checks the cell's further condition.
func guard(d core.Decision, v Invoice, actor string) error {
	switch d.Guard {
	case GuardOrderInvoice:
		if v.Kind != "order" {
			return core.ErrState.New("an expense invoice is not matched: it is approved")
		}
	case GuardExpenseInvoice:
		if v.Kind != "expense" {
			return core.ErrState.New("an order invoice is matched first; only a mismatch is approved")
		}
		return notMaker(v, actor)
	case GuardNotMaker:
		return notMaker(v, actor)
	case GuardExpenseApproved:
		if v.Kind != "expense" {
			return core.ErrState.New("an order invoice posts once matched")
		}
		if v.DifferenceApprovedBy == nil {
			return core.ErrState.New("an expense invoice posts once approved")
		}
	case GuardApproved:
		if v.DifferenceApprovedBy == nil {
			return core.ErrState.New("the invoice's difference is not approved")
		}
	}
	return nil
}

func notMaker(v Invoice, actor string) error {
	if v.MakerID == actor {
		return core.ErrApprover.New("the invoice was registered by " + actor + ": another approves it")
	}
	return nil
}

// match is SI02 in the transaction: the lines against the basis taken before it and the invoiced quantities.
func match(m *core.Base, ctx context.Context, tx db.Tx, v Invoice, ev evidence) error {
	invoiced, err := invoicedOf(ctx, tx, v.orderLines())
	if err != nil {
		return err
	}
	results, ok := evaluate(v, ev.basis, invoiced, m.ToleranceOf(v.LegalEntityID))
	return record(ctx, tx, v, results, ok)
}

// setState is the one writer of the invoice's state besides record (a matching's outcome).
func setState(ctx context.Context, tx db.Tx, v Invoice, to string) error {
	tag, err := tx.Exec(ctx, `UPDATE supplier_invoice SET state = $2, version = version + 1, updated_at = now()
		WHERE id = $1 AND state = $3`, v.ID, to, v.State)
	if err == nil && tag.RowsAffected() != 1 {
		err = fmt.Errorf("supplierinvoices: %s is no longer %s", v.ID, v.State)
	}
	return err
}
