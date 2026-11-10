//go:build integration

package itest

import (
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/ids"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// claims: erp/payment's payment claims on open items (docs/design.md, the claim table; CL01-CL06, CL08; protocol 05's
// claim pattern) over the system surface, and the due open items for its proposals.
func (fx *fixture) claims() {
	t := fx.t
	pay := fx.claimClient(core.Payment)
	payable := fx.payable // 1585.2

	// CL01: claimed within the available balance; CL02: the same claim again is the same, another amount conflicts
	c1 := ids.New().String()
	c := fx.claim(pay, c1, payable, "600")
	assert.Equal(t, "pending", c.GetState())
	exp, err := time.Parse(time.RFC3339Nano, c.GetExpiresAt())
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(4*time.Second), exp, 2*time.Second)
	assert.Equal(t, "600", fx.claim(pay, c1, payable, "600").GetAmount())
	fx.claimRefused(pay, c1, payable, "500", "CLAIM_CONFLICT")
	fx.claimRefused(pay, ids.New().String(), payable, "1000", "CLAIM_EXCEEDS_AVAILABLE") // 985.2 left
	o := fx.get("/api/finance/open-items/" + payable)
	assert.Equal(t, "600", o["pay_claimed"])
	assert.Equal(t, "985.2", o["available"])
	assert.Equal(t, c1, at(o, "claims", 0, "claim_id"))
	// only erp/payment claims
	_, err = fx.claimClient("erp/credit").Claim(fx.ctx, &financev3.ClaimRequest{ClaimId: ids.New().String(), OpenItemId: payable, Amount: "1"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	// CL01: a receivable is not paid
	var receivable string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT id::text FROM "+itSchema+".open_item WHERE kind = 'receivable' LIMIT 1").Scan(&receivable))
	fx.claimRefused(pay, ids.New().String(), receivable, "1", "CLAIM_STATE")

	// CL03: renewed while pending; CL04: confirmed, then it no longer renews nor expires; confirming again is ignored
	r, err := pay.Renew(fx.ctx, &financev3.RenewRequest{ClaimId: c1})
	require.NoError(t, err)
	renewed, _ := time.Parse(time.RFC3339Nano, r.GetClaim().GetExpiresAt())
	assert.True(t, renewed.After(exp), "%s after %s", renewed, exp)
	cf, err := pay.Confirm(fx.ctx, &financev3.ConfirmRequest{ClaimId: c1})
	require.NoError(t, err)
	assert.Equal(t, "confirmed", cf.GetClaim().GetState())
	assert.Empty(t, cf.GetClaim().GetExpiresAt())
	_, err = pay.Confirm(fx.ctx, &financev3.ConfirmRequest{ClaimId: c1})
	assert.NoError(t, err)
	_, err = pay.Renew(fx.ctx, &financev3.RenewRequest{ClaimId: c1})
	fx.claimReason(err, "CLAIM_STATE")

	// CL06: a pending claim released gives its amount back; CL08: released again succeeds, confirming it is refused
	c3 := ids.New().String()
	fx.claim(pay, c3, payable, "300")
	rel, err := pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c3})
	require.NoError(t, err)
	assert.Equal(t, "released", rel.GetClaim().GetState())
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c3})
	assert.NoError(t, err)
	_, err = pay.Confirm(fx.ctx, &financev3.ConfirmRequest{ClaimId: c3})
	fx.claimReason(err, "CLAIM_STATE")
	fx.claimRefused(pay, c3, payable, "300", "CLAIM_STATE")
	assert.Equal(t, "985.2", fx.get("/api/finance/open-items/"+payable)["available"])

	// CL03: an unknown claim: Get answers NOT_FOUND, the commands CLAIM_STATE
	_, err = pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: ids.New().String()})
	assert.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: ids.New().String()})
	fx.claimReason(err, "CLAIM_STATE")

	// CL05: a pending claim expires after its period, its amount back; the confirmed one stays
	c4 := ids.New().String()
	fx.claim(pay, c4, payable, "100")
	assert.Equal(t, "885.2", fx.get("/api/finance/open-items/"+payable)["available"])
	require.Eventually(t, func() bool {
		g, err := pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: c4})
		return err == nil && g.GetClaim().GetState() == "expired"
	}, 20*time.Second, 200*time.Millisecond)
	o = fx.get("/api/finance/open-items/" + payable)
	assert.Equal(t, "985.2", o["available"])
	assert.Equal(t, "600", o["pay_claimed"])
	g, err := pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: c1})
	require.NoError(t, err)
	assert.Equal(t, "confirmed", g.GetClaim().GetState())
	fx.claimRefused(pay, c4, payable, "100", "CLAIM_STATE") // CL08: after its expiry a new claim ID claims again
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c4})
	assert.NoError(t, err) // CL08: releasing an expired claim succeeds
	// CL03, CL05: a claim renewed halfway outlives its first expiry (the first expiry's work item finds it renewed)
	c5 := ids.New().String()
	first, _ := time.Parse(time.RFC3339Nano, fx.claim(pay, c5, payable, "50").GetExpiresAt())
	time.Sleep(2 * time.Second)
	_, err = pay.Renew(fx.ctx, &financev3.RenewRequest{ClaimId: c5})
	require.NoError(t, err)
	time.Sleep(time.Until(first) + 1500*time.Millisecond)
	g, err = pay.Get(fx.ctx, &financev3.GetRequest{ClaimId: c5})
	require.NoError(t, err)
	assert.Equal(t, "pending", g.GetClaim().GetState(), "renewed: not expired at its first expiry")
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c5})
	require.NoError(t, err)
	// CL06: the confirmed claim released by its claimant
	rel, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: c1})
	require.NoError(t, err)
	assert.Equal(t, "released", rel.GetClaim().GetState())
	assert.Equal(t, "1585.2", fx.get("/api/finance/open-items/"+payable)["available"])

	// the due open items: Shanghai's payables due today, earliest first, a page of one at a time
	today := fx.today()
	var seen []string
	cursor := ""
	for i := 0; i < 10; i++ {
		d, err := pay.DueItems(fx.ctx, &financev3.DueItemsRequest{LegalEntityId: fx.le, Kind: "payable", DueOnOrBefore: today,
			Cursor: cursor, Limit: 1})
		require.NoError(t, err)
		for _, it := range d.GetItems() {
			seen = append(seen, it.GetId())
			assert.Equal(t, "payable", it.GetKind())
		}
		if cursor = d.GetNextCursor(); cursor == "" {
			break
		}
	}
	assert.Len(t, seen, 3, "the three posted supplier invoices")
	assert.Contains(t, seen, payable)
	// a payable claimed whole has nothing available: not due for a proposal
	whole := ids.New().String()
	fx.claim(pay, whole, payable, "1585.2")
	d, err := pay.DueItems(fx.ctx, &financev3.DueItemsRequest{LegalEntityId: fx.le, Kind: "payable", DueOnOrBefore: today})
	require.NoError(t, err)
	assert.Len(t, d.GetItems(), 2)
	_, err = pay.Release(fx.ctx, &financev3.ReleaseRequest{ClaimId: whole})
	require.NoError(t, err)
	d, err = pay.DueItems(fx.ctx, &financev3.DueItemsRequest{LegalEntityId: fx.le, Kind: "payable", DueOnOrBefore: month(-1)})
	require.NoError(t, err)
	assert.Empty(t, d.GetItems(), "nothing was due a month ago")
}

func (fx *fixture) claimClient(as string) financev3.ClaimServiceClient {
	fx.t.Helper()
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(fx.t, as).ClientConfig(core.ID))))
	require.NoError(fx.t, err)
	fx.t.Cleanup(func() { conn.Close() })
	return financev3.NewClaimServiceClient(conn)
}

func (fx *fixture) claim(c financev3.ClaimServiceClient, id, item, amount string) *financev3.Claim {
	fx.t.Helper()
	r, err := c.Claim(fx.ctx, &financev3.ClaimRequest{ClaimId: id, OpenItemId: item, Amount: amount})
	require.NoError(fx.t, err)
	return r.GetClaim()
}

func (fx *fixture) claimRefused(c financev3.ClaimServiceClient, id, item, amount, reason string) {
	fx.t.Helper()
	_, err := c.Claim(fx.ctx, &financev3.ClaimRequest{ClaimId: id, OpenItemId: item, Amount: amount})
	fx.claimReason(err, reason)
}

// claimReason asserts a refusal: FailedPrecondition with the reason in its error info.
func (fx *fixture) claimReason(err error, reason string) {
	fx.t.Helper()
	require.Error(fx.t, err)
	assert.Equal(fx.t, codes.FailedPrecondition, status.Code(err), "%v", err)
	e, _, ok := errs.FromGRPC(err)
	require.True(fx.t, ok, "%v", err)
	assert.Equal(fx.t, reason, e.Reason, "%v", err)
}
