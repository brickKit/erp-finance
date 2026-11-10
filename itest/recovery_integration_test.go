//go:build integration

package itest

import (
	"strconv"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/recoverypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// recoveryChecks: a full recovery asks this member, as the recovery tool only, whether the objects its audit names are
// still there at their versions (be-protocol chapter 14, P14.3), each type it audits, and runs its checks: the facts
// received but not posted (docs/design.md, RC01).
func (fx *fixture) recoveryChecks() {
	t := fx.t
	dial := func(as string) *grpc.ClientConn {
		conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, as).ClientConfig(core.ID))))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	_, err := recoverypb.NewCheckServiceClient(dial(core.Payment)).Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "the recovery tool only: %v", err)
	cs := recoverypb.NewCheckServiceClient(dial("ops/recover"))
	for _, typ := range []string{core.Accounts, core.Vouchers, core.Periods, core.OpenItems, core.SupplierInvoices, core.System} {
		rows, err := fx.admin.Query(fx.ctx, "SELECT DISTINCT ON (target->>'id') target->>'id', target->>'version' FROM "+
			itSchema+".be_audit_log WHERE target->>'type' = $1 ORDER BY target->>'id', ord DESC", typ)
		require.NoError(t, err)
		var refs []*recoverypb.ObjectRef
		for rows.Next() {
			var id, version string
			require.NoError(t, rows.Scan(&id, &version))
			refs = append(refs, &recoverypb.ObjectRef{Type: typ, Id: id, Version: version})
		}
		rows.Close()
		require.NotEmpty(t, refs, "%s is audited", typ)
		// every audited object is present at its last audited version or later; one version on is older; a new ID absent
		present, err := cs.Present(fx.ctx, &recoverypb.PresentRequest{Recovery: "r", Objects: refs})
		require.NoError(t, err, typ)
		for i, a := range present.GetAnswers() {
			assert.Equal(t, "present", a.GetAnswer(), "%s %s", typ, refs[i].GetId())
		}
		last := refs[0]
		v, err := strconv.ParseInt(last.GetVersion(), 10, 64)
		require.NoError(t, err, "%s %s has a version", typ, last.GetId())
		present, err = cs.Present(fx.ctx, &recoverypb.PresentRequest{Recovery: "r", Objects: []*recoverypb.ObjectRef{
			{Type: typ, Id: last.GetId(), Version: strconv.FormatInt(v+1000, 10)}, {Type: typ, Id: ids.New().String(), Version: "1"}}})
		require.NoError(t, err, typ)
		assert.Equal(t, "older", present.GetAnswers()[0].GetAnswer(), typ)
		assert.Equal(t, "absent", present.GetAnswers()[1].GetAnswer(), typ)
	}
	// RC01: the two accounting facts that wait (an unknown rule, unbalanced) are reported
	res, err := cs.Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	require.NoError(t, err)
	var pending *recoverypb.CheckResult
	for _, r := range res.GetResults() {
		if r.GetCheck() == "erp.finance.pending_facts" {
			pending = r
		}
	}
	require.NotNil(t, pending, "the check is registered")
	assert.Equal(t, "diff", pending.GetOutcome())
	assert.Len(t, pending.GetDifferences(), 2, pending.GetDifferences())
	assert.NotEmpty(t, pending.GetDisposal())
}
