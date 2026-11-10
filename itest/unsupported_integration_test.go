//go:build integration

package itest

import (
	"github.com/stretchr/testify/assert"
)

// notInThisVersion: the routes of the contract this version does not serve answer CAPABILITY_UNSUPPORTED (D75;
// docs/design.md, "实现状态").
func (fx *fixture) notInThisVersion() {
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/finance/closings"},
		{"POST", "/api/finance/revaluations"},
		{"GET", "/api/finance/release-ledgers/" + fx.le},
	} {
		code, out := fx.alice.do(r.method, r.path, map[string]any{})
		assert.Equal(fx.t, "CAPABILITY_UNSUPPORTED", out["reason"], "%s %s: %d %v", r.method, r.path, code, out)
	}
}
