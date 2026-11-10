package periods

import (
	"regexp"

	core "github.com/brickKit/erp-finance/v3/internal/core"
)

// The period's lifecycle in one table (docs/design.md, the period table; PD01, PD02). The first batch has no closing
// batch (no participant seals yet): closing is this component's own act.

const (
	Open     = "open"
	Closed   = "closed"
	Reopened = "reopened"
)

const (
	Close      = "close"
	Reopen     = "reopen"
	PostManual = "post_manual"
)

const (
	// GuardNothingWaiting: no manual voucher of the period waits for approval, every earlier month is closed.
	GuardNothingWaiting = "no manual voucher waiting, the earlier months closed"
	// GuardReason: a reopening says why.
	GuardReason = "a reason"
)

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^PD\d\d$`),
	Phases:   []string{Open, Closed, Reopened},
	Commands: []string{Close, Reopen, PostManual},
	Cells: map[string]map[string]core.Decision{
		Close: {
			Open:     {Rule: "PD02", Effect: core.Move, To: Closed, Op: core.OpPeriodClose, Guard: GuardNothingWaiting},
			Closed:   core.IgnoreCell("PD02"),
			Reopened: {Rule: "PD02", Effect: core.Move, To: Closed, Op: core.OpPeriodClose, Guard: GuardNothingWaiting},
		},
		Reopen: {
			Open:     core.RefuseCell("PD02", "only a closed period is reopened; this one is {state}"),
			Closed:   {Rule: "PD02", Effect: core.Move, To: Reopened, Op: core.OpPeriodClose, Guard: GuardReason},
			Reopened: core.IgnoreCell("PD02"),
		},
		PostManual: {
			Open:     core.ProceedCell("PD01"),
			Closed:   core.RefuseCell("PD01", "the period is {state}: reopen it to post there"),
			Reopened: core.ProceedCell("PD01"),
		},
	},
}
