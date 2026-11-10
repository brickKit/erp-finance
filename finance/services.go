package finance

import (
	"context"

	facts "github.com/brickKit/erp-finance/v3/internal/facts"

	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
)

// Read is FactService.Read: this component's incremental facts by identity (internal/facts).
func (m *Module) Read(ctx context.Context, in *financev3.ReadRequest) (*financev3.ReadResponse, error) {
	return facts.Read(m.b, ctx, in)
}
