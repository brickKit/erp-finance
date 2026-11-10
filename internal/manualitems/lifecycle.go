package manualitems

import (
	"regexp"

	core "github.com/brickKit/erp-finance/v3/internal/core"
)

// A manual open item's approval in one table (docs/design.md, the manual open item table; MI01-MI03). The stored
// state only: settled is read from the open balance, not a phase here.

const (
	PendingApproval = "pending_approval"
	Open            = "open"
	Cancelled       = "cancelled"
)

const (
	Approve = "approve"
	Cancel  = "cancel"
)

// GuardNotMaker: the approver is not the one who registered it (by account; by natural person with C7).
const GuardNotMaker = "not by the one who registered it"

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^MI\d\d$`),
	Phases:   []string{PendingApproval, Open, Cancelled},
	Commands: []string{Approve, Cancel},
	Cells: map[string]map[string]core.Decision{
		Approve: {
			PendingApproval: {Rule: "MI02", Effect: core.Move, To: Open, Op: core.OpOpenItemApprove, Guard: GuardNotMaker},
			Open:            core.IgnoreCell("MI02"),
			Cancelled:       core.RefuseCell("MI03", "a cancelled open item is not approved"),
		},
		Cancel: {
			PendingApproval: core.MoveCell("MI03", Cancelled, core.OpOpenItemEdit),
			Open:            core.RefuseCell("MI03", "an approved open item does not cancel: a reverse one corrects it"),
			Cancelled:       core.IgnoreCell("MI03"),
		},
	},
}
