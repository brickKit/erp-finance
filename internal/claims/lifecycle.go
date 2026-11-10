package claims

import (
	"regexp"

	core "github.com/brickKit/erp-finance/v3/internal/core"
)

// The payment claim's lifecycle in one table (docs/design.md, the claim table; protocol 05's claim pattern, the payment
// use; CL01-CL09). None is a claim ID not known yet. Every command asks Lifecycle.Decide; the state changes only through
// move.

const (
	None       = "none"
	Pending    = "pending"
	Confirmed  = "confirmed"
	Formalized = "formalized"
	Expired    = "expired"
	Released   = "released"
)

const (
	ClaimCmd     = "claim"
	Renew        = "renew"
	Confirm      = "confirm"
	Expire       = "expire"
	Release      = "release"
	FormalizeCmd = "formalize"
	ReverseCmd   = "reverse"
)

const (
	// GuardAvailable: the amount within the open item's available balance (CL01)
	GuardAvailable = "within the available balance"
	// GuardSameAmount: the same claim again (same amount, same open item) is ignored; another is CLAIM_CONFLICT (CL02, CL07)
	GuardSameAmount = "the same amount"
	// GuardNotExpired: a renewal before the claim's expiry (CL03)
	GuardNotExpired = "before its expiry"
	// GuardPastExpiry: the expiry's work item acts only once the period has passed (a renewal moved it, CL05)
	GuardPastExpiry = "its period passed"
	// GuardClaimant: a confirmed claim is released by its claimant only (CL06)
	GuardClaimant = "by its claimant"
	// GuardWithinFormalized: the reversals in all within the formalized amount (CL09)
	GuardWithinFormalized = "within the formalized amount"
)

const (
	unknown = "no such claim"
	done    = "the claim is {state}: a new claim ID claims again"
)

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^CL\d\d$`),
	Phases:   []string{None, Pending, Confirmed, Formalized, Expired, Released},
	Commands: []string{ClaimCmd, Renew, Confirm, Expire, Release, FormalizeCmd, ReverseCmd},
	Cells: map[string]map[string]core.Decision{
		ClaimCmd: {
			None:       {Rule: "CL01", Effect: core.Move, To: Pending, Op: core.OpSysClaim, Guard: GuardAvailable},
			Pending:    {Rule: "CL02", Effect: core.Ignore, Guard: GuardSameAmount},
			Confirmed:  {Rule: "CL02", Effect: core.Ignore, Guard: GuardSameAmount},
			Formalized: {Rule: "CL02", Effect: core.Ignore, Guard: GuardSameAmount},
			Expired:    core.RefuseCell("CL08", done),
			Released:   core.RefuseCell("CL08", done),
		},
		Renew: {
			None:       core.RefuseCell("CL03", unknown),
			Pending:    {Rule: "CL03", Effect: core.Proceed, Guard: GuardNotExpired},
			Confirmed:  core.RefuseCell("CL03", "only a pending claim is renewed; this one is {state}"),
			Formalized: core.RefuseCell("CL03", "only a pending claim is renewed; this one is {state}"),
			Expired:    core.RefuseCell("CL08", done),
			Released:   core.RefuseCell("CL08", done),
		},
		Confirm: {
			None:       core.RefuseCell("CL03", unknown),
			Pending:    core.MoveCell("CL04", Confirmed, core.OpSysClaim),
			Confirmed:  core.IgnoreCell("CL04"),
			Formalized: core.IgnoreCell("CL04"),
			Expired:    core.RefuseCell("CL08", done),
			Released:   core.RefuseCell("CL08", done),
		},
		Expire: {
			None:       core.IgnoreCell("CL05"),
			Pending:    {Rule: "CL05", Effect: core.Move, To: Expired, Op: core.OpSysClaim, Guard: GuardPastExpiry},
			Confirmed:  core.IgnoreCell("CL05"),
			Formalized: core.IgnoreCell("CL05"),
			Expired:    core.IgnoreCell("CL05"),
			Released:   core.IgnoreCell("CL05"),
		},
		Release: {
			None:       core.RefuseCell("CL03", unknown),
			Pending:    core.MoveCell("CL06", Released, core.OpSysClaim),
			Confirmed:  {Rule: "CL06", Effect: core.Move, To: Released, Op: core.OpSysClaim, Guard: GuardClaimant},
			Formalized: core.RefuseCell("CL06", "a formalized claim is not released"),
			Expired:    core.IgnoreCell("CL08"),
			Released:   core.IgnoreCell("CL08"),
		},
		FormalizeCmd: {
			None:       core.RefuseCell("CL03", unknown),
			Pending:    core.RefuseCell("CL07", "a claim is formalized once confirmed"),
			Confirmed:  core.MoveCell("CL07", Formalized, core.OpSysClaim),
			Formalized: {Rule: "CL07", Effect: core.Ignore, Guard: GuardSameAmount},
			Expired:    core.RefuseCell("CL08", done),
			Released:   core.RefuseCell("CL08", done),
		},
		ReverseCmd: {
			None:       core.RefuseCell("CL03", unknown),
			Pending:    core.RefuseCell("CL09", "only a formalized claim is reversed"),
			Confirmed:  core.RefuseCell("CL09", "only a formalized claim is reversed"),
			Formalized: {Rule: "CL09", Effect: core.Proceed, Guard: GuardWithinFormalized},
			Expired:    core.RefuseCell("CL08", done),
			Released:   core.RefuseCell("CL08", done),
		},
	},
}
