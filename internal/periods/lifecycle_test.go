package periods

import (
	"os"
	"testing"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheLifecycleDecidesEveryCell: every state decides every command, each cell complete for its effect.
func TestTheLifecycleDecidesEveryCell(t *testing.T) {
	assert.Empty(t, core.TableProblems(Lifecycle))
}

// TestTheDesignDocumentIsTheLifecycle: the table in docs/design.md ("手工凭证与期间") is this table, cell by cell.
func TestTheDesignDocumentIsTheLifecycle(t *testing.T) {
	doc, err := os.ReadFile("../../docs/design.md")
	require.NoError(t, err)
	assert.Empty(t, core.DocumentProblems(Lifecycle, string(doc)))
}

// TestEveryRuleIsTested: each rule of the table is named by an integration test.
func TestEveryRuleIsTested(t *testing.T) {
	untested, err := core.UntestedRules(Lifecycle, "../../itest")
	require.NoError(t, err)
	assert.Empty(t, untested)
}
