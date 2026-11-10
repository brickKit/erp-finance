package supplierinvoices

import (
	"regexp"

	core "github.com/brickKit/erp-finance/v3/internal/core"
)

// The supplier invoice's lifecycle in one table (docs/design.md, the supplier invoice table; SI01-SI05). Every command
// asks Lifecycle.Decide; the state changes only through setState.

const (
	Draft     = "draft"
	Matched   = "matched"
	Mismatch  = "mismatch"
	Posted    = "posted"
	Cancelled = "cancelled"
)

const (
	Edit    = "edit"
	Match   = "match"
	Approve = "approve"
	Post    = "post"
	Cancel  = "cancel"
)

const (
	// GuardOrderInvoice: an order invoice matches; an expense invoice has nothing to match
	GuardOrderInvoice = "an order invoice"
	// GuardExpenseInvoice: in draft only an expense invoice is approved (an order invoice matches first)
	GuardExpenseInvoice = "an expense invoice"
	// GuardExpenseApproved: a draft posts when it is an expense invoice approved
	GuardExpenseApproved = "an expense invoice approved"
	// GuardApproved: a mismatch posts once its difference is approved
	GuardApproved = "its difference approved"
	// GuardNotMaker: the approver is not the one who registered it (by account; by natural person with C7)
	GuardNotMaker = "not by the one who registered it"
)

const posted = "a posted invoice does not change: a supplier credit corrects it (later)"

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^SI\d\d$`),
	Phases:   []string{Draft, Matched, Mismatch, Posted, Cancelled},
	Commands: []string{Edit, Match, Approve, Post, Cancel},
	Cells: map[string]map[string]core.Decision{
		Edit: {
			Draft:     core.ProceedCell("SI01"),
			Matched:   core.MoveCell("SI01", Draft, core.OpSupplierInvEdit),
			Mismatch:  core.MoveCell("SI01", Draft, core.OpSupplierInvEdit),
			Posted:    core.RefuseCell("SI05", posted),
			Cancelled: core.RefuseCell("SI05", "a cancelled invoice does not change"),
		},
		Match: {
			Draft:     {Rule: "SI02", Effect: core.Proceed, Guard: GuardOrderInvoice},
			Matched:   core.ProceedCell("SI02"),
			Mismatch:  core.ProceedCell("SI02"),
			Posted:    core.RefuseCell("SI05", posted),
			Cancelled: core.RefuseCell("SI05", "a cancelled invoice is not matched"),
		},
		Approve: {
			Draft:     {Rule: "SI04", Effect: core.Proceed, Guard: GuardExpenseInvoice},
			Matched:   core.RefuseCell("SI04", "a matched invoice has no difference to approve"),
			Mismatch:  {Rule: "SI04", Effect: core.Proceed, Guard: GuardNotMaker},
			Posted:    core.RefuseCell("SI05", posted),
			Cancelled: core.RefuseCell("SI05", "a cancelled invoice is not approved"),
		},
		Post: {
			Draft:     {Rule: "SI03", Effect: core.Move, To: Posted, Op: core.OpSupplierInvEdit, Guard: GuardExpenseApproved},
			Matched:   core.MoveCell("SI03", Posted, core.OpSupplierInvEdit),
			Mismatch:  {Rule: "SI04", Effect: core.Move, To: Posted, Op: core.OpSupplierInvEdit, Guard: GuardApproved},
			Posted:    core.IgnoreCell("SI03"),
			Cancelled: core.RefuseCell("SI05", "a cancelled invoice is not posted"),
		},
		Cancel: {
			Draft:     core.MoveCell("SI05", Cancelled, core.OpSupplierInvEdit),
			Matched:   core.MoveCell("SI05", Cancelled, core.OpSupplierInvEdit),
			Mismatch:  core.MoveCell("SI05", Cancelled, core.OpSupplierInvEdit),
			Posted:    core.RefuseCell("SI05", posted),
			Cancelled: core.IgnoreCell("SI05"),
		},
	},
}
