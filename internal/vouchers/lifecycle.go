package vouchers

import (
	"context"
	"fmt"
	"regexp"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"

	"github.com/brickKit/be-sdk-go/v3/db"
)

// The manual voucher's lifecycle in one table (docs/design.md, the voucher table; VC01-VC04). A system voucher is
// posted from its fact at once and never passes through this table. Every command asks Lifecycle.Decide; every
// change of the state goes through Transition (posting through ledger.Issue, which numbers it).

const (
	Draft     = "draft"
	Submitted = "submitted"
	Posted    = "posted"
	Reversed  = "reversed"
)

const (
	Edit    = "edit"
	Submit  = "submit"
	Approve = "approve"
	Reject  = "reject"
	Reverse = "reverse"
)

const (
	// GuardNotMaker: the approver is not the one who made it (by account; by natural person with C7).
	GuardNotMaker = "not by the one who made it"
)

const posted = "a posted voucher does not change: reverse it"

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^VC\d\d$`),
	Phases:   []string{Draft, Submitted, Posted, Reversed},
	Commands: []string{Edit, Submit, Approve, Reject, Reverse},
	Cells: map[string]map[string]core.Decision{
		Edit: {
			Draft:     core.ProceedCell("VC01"),
			Submitted: core.RefuseCell("VC02", "a submitted voucher does not change: reject it first"),
			Posted:    core.RefuseCell("VC04", posted),
			Reversed:  core.RefuseCell("VC04", posted),
		},
		Submit: {
			Draft:     core.MoveCell("VC01", Submitted, core.OpVoucherEdit),
			Submitted: core.RefuseCell("VC02", "only a draft is submitted; this one is {state}"),
			Posted:    core.RefuseCell("VC04", posted),
			Reversed:  core.RefuseCell("VC04", posted),
		},
		Approve: {
			Draft:     core.RefuseCell("VC02", "only a submitted voucher is approved; this one is {state}"),
			Submitted: {Rule: "VC02", Effect: core.Move, To: Posted, Op: core.OpVoucherApprove, Guard: GuardNotMaker},
			Posted:    core.IgnoreCell("VC02"),
			Reversed:  core.RefuseCell("VC04", "a reversed voucher is not approved"),
		},
		Reject: {
			Draft:     core.RefuseCell("VC02", "only a submitted voucher is rejected; this one is {state}"),
			Submitted: core.MoveCell("VC02", Draft, core.OpVoucherApprove),
			Posted:    core.RefuseCell("VC02", "only a submitted voucher is rejected; this one is {state}"),
			Reversed:  core.RefuseCell("VC04", posted),
		},
		Reverse: {
			Draft:     core.RefuseCell("VC04", "only a posted voucher is reversed; this one is {state}"),
			Submitted: core.RefuseCell("VC04", "only a posted voucher is reversed; this one is {state}"),
			Posted:    core.MoveCell("VC04", Reversed, core.OpVoucherApprove),
			Reversed:  core.IgnoreCell("VC04"),
		},
	},
}

// Transition is the one writer of a manual voucher's state besides ledger.Issue (which posts it): d is a Move
// decided for v, read under its lock in tx.
func Transition(m *core.Base, ctx context.Context, tx db.Tx, v ledger.Voucher, d core.Decision, reason *string,
	reversedBy *string, actor string) (ledger.Voucher, error) {
	if d.Effect != core.Move {
		return v, fmt.Errorf("vouchers: transition of %s on a %s decision", v.ID, d.Effect)
	}
	tag, err := tx.Exec(ctx, `UPDATE voucher SET state = $2, reason = coalesce($3, reason), reversed_by_id = coalesce($4,
		reversed_by_id), version = version + 1, updated_at = now() WHERE id = $1 AND state = $5`,
		v.ID, d.To, reason, reversedBy, v.State)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() != 1 {
		return v, fmt.Errorf("vouchers: %s is no longer %s", v.ID, v.State)
	}
	after, err := ledger.Get(ctx, tx, v.ID, false)
	if err != nil {
		return after, err
	}
	return after, core.Change(m, ctx, tx, actor, d.Op, core.Vouchers, v.ID, after)
}
