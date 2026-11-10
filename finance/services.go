package finance

import (
	"context"

	claims "github.com/brickKit/erp-finance/v3/internal/claims"
	facts "github.com/brickKit/erp-finance/v3/internal/facts"

	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
)

// Read is FactService.Read: this component's incremental facts by identity (internal/facts).
func (m *Module) Read(ctx context.Context, in *financev3.ReadRequest) (*financev3.ReadResponse, error) {
	return facts.Read(m.b, ctx, in)
}

// ClaimService (internal/claims): erp/payment's payment claims and the due open items.

func (m *Module) Claim(ctx context.Context, in *financev3.ClaimRequest) (*financev3.ClaimResponse, error) {
	return claims.Claim(m.b, ctx, in)
}

func (m *Module) Renew(ctx context.Context, in *financev3.RenewRequest) (*financev3.RenewResponse, error) {
	return claims.RenewClaim(m.b, ctx, in)
}

func (m *Module) Confirm(ctx context.Context, in *financev3.ConfirmRequest) (*financev3.ConfirmResponse, error) {
	return claims.ConfirmClaim(m.b, ctx, in)
}

func (m *Module) Release(ctx context.Context, in *financev3.ReleaseRequest) (*financev3.ReleaseResponse, error) {
	return claims.ReleaseClaim(m.b, ctx, in)
}

func (m *Module) Get(ctx context.Context, in *financev3.GetRequest) (*financev3.GetResponse, error) {
	return claims.GetClaim(m.b, ctx, in)
}

func (m *Module) DueItems(ctx context.Context, in *financev3.DueItemsRequest) (*financev3.DueItemsResponse, error) {
	return claims.DueItems(m.b, ctx, in)
}
