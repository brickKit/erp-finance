package claims

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/work"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	"github.com/jackc/pgx/v5"
)

// JobExpireClaim expires a pending claim at its expiry (CL05).
const JobExpireClaim = "expire-claim"

type expireArgs struct {
	ClaimID string `json:"claim_id"`
}

// Record is a pay_claim row.
type Record struct {
	ClaimID          string
	OpenItemID       string
	Claimant         string
	Amount           string
	State            string
	ExpiresAt        *time.Time
	FormalizedAmount string
	ReversedAmount   string
	Version          int64
}

const cols = `claim_id, open_item_id::text, claimant, trim_scale(amount)::text, state, expires_at,
	trim_scale(formalized_amount)::text, trim_scale(reversed_amount)::text, version`

// Get reads a claim in tx under its lock; an unknown claim is in the state none.
func Get(ctx context.Context, tx db.Tx, id string) (Record, error) {
	r := Record{ClaimID: id, State: None}
	err := tx.QueryRow(ctx, `SELECT `+cols+` FROM pay_claim WHERE claim_id = $1 FOR UPDATE`, id).Scan(&r.ClaimID,
		&r.OpenItemID, &r.Claimant, &r.Amount, &r.State, &r.ExpiresAt, &r.FormalizedAmount, &r.ReversedAmount, &r.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{ClaimID: id, State: None}, nil
	}
	return r, err
}

func (r Record) proto() *financev3.Claim {
	c := &financev3.Claim{ClaimId: r.ClaimID, OpenItemId: r.OpenItemID, Amount: r.Amount, State: r.State,
		FormalizedAmount: r.FormalizedAmount, ReversedAmount: r.ReversedAmount}
	if r.ExpiresAt != nil && r.State == Pending {
		c.ExpiresAt = canon.Time(*r.ExpiresAt)
	}
	return c
}

// refused is a refusal of the claim table as the system surface answers it (CLAIM_STATE, FailedPrecondition).
func refused(d core.Decision, state string) error {
	return core.ErrClaimState.New(strings.ReplaceAll(d.Refusal, "{state}", state))
}

// adjust moves an open item's pay_claimed by delta (a claim taken: +, given back: -), in the claim's transaction.
func adjust(ctx context.Context, tx db.Tx, item, delta string) error {
	_, err := tx.Exec(ctx, `UPDATE open_item SET pay_claimed = pay_claimed + $2, version = version + 1, updated_at = now()
		WHERE id = $1`, item, delta)
	return err
}

// move is the one writer of a claim's state (besides the insertion of a new one): with the open item's pay_claimed
// given back when the claim leaves pending or confirmed for expired or released, and the audit.
func move(m *core.Base, ctx context.Context, tx db.Tx, r Record, d core.Decision, actor string) (Record, error) {
	if _, err := tx.Exec(ctx, `UPDATE pay_claim SET state = $2, expires_at = CASE WHEN $2 = 'pending' THEN expires_at END,
		version = version + 1, updated_at = now() WHERE claim_id = $1`, r.ClaimID, d.To); err != nil {
		return r, err
	}
	if d.To == Expired || d.To == Released {
		if err := adjust(ctx, tx, r.OpenItemID, core.Minus("0", r.Amount)); err != nil {
			return r, err
		}
	}
	after, err := Get(ctx, tx, r.ClaimID)
	if err != nil {
		return after, err
	}
	return after, change(m, ctx, tx, actor, after)
}

// change audits the claim as the system surface answers it, with its version (a full recovery asks for it, P14.3).
func change(m *core.Base, ctx context.Context, tx db.Tx, actor string, r Record) error {
	after := map[string]any{}
	b, _ := json.Marshal(r.proto())
	if err := json.Unmarshal(b, &after); err != nil {
		return err
	}
	after["version"] = r.Version
	return core.Change(m, ctx, tx, actor, core.OpSysClaim, core.System, r.ClaimID, after)
}

// expireAt queues the claim's expiry at its expiry instant (CL05).
func expireAt(m *core.Base, ctx context.Context, tx db.Tx, id string, at time.Time) error {
	_, err := m.RT.Work.EnqueueAt(ctx, tx, JobExpireClaim, expireArgs{ClaimID: id}, at)
	return err
}

// Claim takes an amount of a payable's or a credit balance's available balance (CL01, CL02).
func Claim(m *core.Base, ctx context.Context, in *financev3.ClaimRequest) (*financev3.ClaimResponse, error) {
	caller, _ := grpcx.Caller(ctx)
	if in.GetClaimId() == "" || len(in.GetClaimId()) > 200 {
		return nil, core.Invalid("claim_id", "claim_id is the claimant's identity of the claim, at most 200 characters")
	}
	item, err := openitems.Read(m, ctx, in.GetOpenItemId())
	if err != nil {
		return nil, err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, item.Currency)
	if err != nil {
		return nil, err
	}
	amount, err := core.Decimal("amount", in.GetAmount(), scale, true)
	if err != nil {
		return nil, err
	}
	var out Record
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, in.GetClaimId())
		if err != nil {
			return err
		}
		out = r
		d := Lifecycle.Decide(r.State, ClaimCmd)
		switch d.Effect {
		case core.Refuse:
			return refused(d, r.State)
		case core.Ignore: // GuardSameAmount
			if r.OpenItemID != item.ID || core.Cmp(r.Amount, amount) != 0 {
				return core.ErrClaimConflict.New(fmt.Sprintf("claim %s is %s of %s", r.ClaimID, r.Amount, r.OpenItemID))
			}
			return nil
		}
		o, err := openitems.Get(ctx, tx, item.ID, true)
		if err != nil {
			return err
		}
		if (o.Kind != "payable" && o.Kind != "credit_balance") || o.State == "pending_approval" || o.State == "cancelled" {
			return core.ErrClaimState.New("a " + o.Kind + " in " + o.State + " is not claimed for a payment")
		}
		if core.Cmp(amount, o.Available) > 0 { // GuardAvailable
			return core.ErrClaimExceeds.New("the open item has " + o.Available + " available")
		}
		at := time.Now().Add(m.ClaimPeriod)
		if _, err := tx.Exec(ctx, `INSERT INTO pay_claim (claim_id, open_item_id, claimant, amount, state, expires_at)
			VALUES ($1, $2, $3, $4, 'pending', $5)`, r.ClaimID, o.ID, caller, amount, at); err != nil {
			return err
		}
		if err := adjust(ctx, tx, o.ID, amount); err != nil {
			return err
		}
		if err := expireAt(m, ctx, tx, r.ClaimID, at); err != nil {
			return err
		}
		if out, err = Get(ctx, tx, r.ClaimID); err != nil {
			return err
		}
		return change(m, ctx, tx, caller, out)
	})
	if err != nil {
		return nil, err
	}
	return &financev3.ClaimResponse{Claim: out.proto()}, nil
}

// command runs renew, confirm or release on a known claim as the table decides; an ignored command answers the claim
// as it is.
func command(m *core.Base, ctx context.Context, id, cmd string) (Record, error) {
	caller, _ := grpcx.Caller(ctx)
	var out Record
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = r
		d := Lifecycle.Decide(r.State, cmd)
		switch d.Effect {
		case core.Ignore:
			return nil
		case core.Refuse:
			return refused(d, r.State)
		}
		switch d.Guard {
		case GuardNotExpired:
			if r.ExpiresAt == nil || !time.Now().Before(*r.ExpiresAt) {
				return core.ErrClaimState.New("the claim's period has passed")
			}
		case GuardClaimant:
			if r.Claimant != caller {
				return core.ErrClaimState.New("a confirmed claim is released by its claimant " + r.Claimant)
			}
		}
		if cmd == Renew { // CL03: another period from now, its expiry queued again (the earlier item finds it later)
			at := time.Now().Add(m.ClaimPeriod)
			if _, err := tx.Exec(ctx, `UPDATE pay_claim SET expires_at = $2, version = version + 1, updated_at = now()
				WHERE claim_id = $1`, id, at); err != nil {
				return err
			}
			if err := expireAt(m, ctx, tx, id, at); err != nil {
				return err
			}
			if out, err = Get(ctx, tx, id); err != nil {
				return err
			}
			return change(m, ctx, tx, caller, out)
		}
		out, err = move(m, ctx, tx, r, d, caller)
		return err
	})
	return out, err
}

func RenewClaim(m *core.Base, ctx context.Context, in *financev3.RenewRequest) (*financev3.RenewResponse, error) {
	r, err := command(m, ctx, in.GetClaimId(), Renew)
	if err != nil {
		return nil, err
	}
	return &financev3.RenewResponse{Claim: r.proto()}, nil
}

func ConfirmClaim(m *core.Base, ctx context.Context, in *financev3.ConfirmRequest) (*financev3.ConfirmResponse, error) {
	r, err := command(m, ctx, in.GetClaimId(), Confirm)
	if err != nil {
		return nil, err
	}
	return &financev3.ConfirmResponse{Claim: r.proto()}, nil
}

func ReleaseClaim(m *core.Base, ctx context.Context, in *financev3.ReleaseRequest) (*financev3.ReleaseResponse, error) {
	r, err := command(m, ctx, in.GetClaimId(), Release)
	if err != nil {
		return nil, err
	}
	return &financev3.ReleaseResponse{Claim: r.proto()}, nil
}

// GetClaim answers a claim as it stands; an unknown one is NOT_FOUND.
func GetClaim(m *core.Base, ctx context.Context, in *financev3.GetRequest) (*financev3.GetResponse, error) {
	var r Record
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		r, err = Get(ctx, tx, in.GetClaimId())
		return err
	})
	if err != nil {
		return nil, err
	}
	if r.State == None {
		return nil, core.NotFound("claim")
	}
	return &financev3.GetResponse{Claim: r.proto()}, nil
}

// ExpireClaim is the expiry's work item (CL05): under the claim's lock, a pending claim whose period has passed
// expires; one renewed, confirmed or done meanwhile is left as it is.
func ExpireClaim(m *core.Base, ctx context.Context, w *work.Run) error {
	var a expireArgs
	if err := json.Unmarshal(w.Args, &a); err != nil {
		return work.Permanent(err)
	}
	return w.Complete(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, a.ClaimID)
		if err != nil {
			return err
		}
		d := Lifecycle.Decide(r.State, Expire)
		if d.Effect != core.Move || r.ExpiresAt == nil || time.Now().Before(*r.ExpiresAt) { // GuardPastExpiry
			return nil
		}
		_, err = move(m, ctx, tx, r, d, core.ID)
		return err
	})
}

// Formalize turns a confirmed claim into money paid (CL07), in the settlement's transaction (the claim locked by Get):
// formalized at amount, at most the claim's; the whole claim leaves pay_claimed (the settlement adds the allocation
// to settled). The same amount again is ignored; another is CLAIM_CONFLICT.
func Formalize(m *core.Base, ctx context.Context, tx db.Tx, r Record, amount, actor string) (Record, error) {
	d := Lifecycle.Decide(r.State, FormalizeCmd)
	switch d.Effect {
	case core.Refuse:
		return r, refused(d, r.State)
	case core.Ignore: // GuardSameAmount
		if core.Cmp(r.FormalizedAmount, amount) != 0 {
			return r, core.ErrClaimConflict.New("claim " + r.ClaimID + " is formalized at " + r.FormalizedAmount)
		}
		return r, nil
	}
	if core.Cmp(amount, r.Amount) > 0 {
		return r, core.ErrClaimExceeds.New("claim " + r.ClaimID + " holds " + r.Amount)
	}
	if _, err := tx.Exec(ctx, `UPDATE pay_claim SET state = 'formalized', formalized_amount = $2, expires_at = NULL,
		version = version + 1, updated_at = now() WHERE claim_id = $1`, r.ClaimID, amount); err != nil {
		return r, err
	}
	if err := adjust(ctx, tx, r.OpenItemID, core.Minus("0", r.Amount)); err != nil {
		return r, err
	}
	after, err := Get(ctx, tx, r.ClaimID)
	if err != nil {
		return after, err
	}
	return after, change(m, ctx, tx, actor, after)
}

// ReverseFormalized records money of a formalized claim given back (CL09), in the reversal's transaction: the
// reversals in all at most the formalized amount (the settlement takes the allocation back from settled).
func ReverseFormalized(m *core.Base, ctx context.Context, tx db.Tx, r Record, amount, actor string) (Record, error) {
	d := Lifecycle.Decide(r.State, ReverseCmd)
	if d.Effect == core.Refuse {
		return r, refused(d, r.State)
	}
	if core.Cmp(core.Sum(r.ReversedAmount, amount), r.FormalizedAmount) > 0 { // GuardWithinFormalized
		return r, core.ErrClaimState.New("claim " + r.ClaimID + " has " + r.FormalizedAmount + " formalized, " +
			r.ReversedAmount + " reversed")
	}
	if _, err := tx.Exec(ctx, `UPDATE pay_claim SET reversed_amount = reversed_amount + $2, version = version + 1,
		updated_at = now() WHERE claim_id = $1`, r.ClaimID, amount); err != nil {
		return r, err
	}
	after, err := Get(ctx, tx, r.ClaimID)
	if err != nil {
		return after, err
	}
	return after, change(m, ctx, tx, actor, after)
}
