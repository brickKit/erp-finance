package masterdata

import (
	"context"
	"encoding/json"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/errs"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"google.golang.org/grpc"
)

// Every call here runs outside a transaction (protocol P4.20): evidence first, then the transaction. Unreachable is
// never "does not exist" (protocol 07): it answers DEPENDENCY_UNAVAILABLE.

func Conn(m *core.Base, id string) (*grpc.ClientConn, error) {
	c, ok := m.RT.GRPC(id)
	if !ok {
		return nil, errs.New(errs.ReasonDependencyUnavailable, id+" is not reachable")
	}
	return c, nil
}

// LegalEntity is what finance needs of a legal entity.
type LegalEntity struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	State        string `json:"state"`
	Timezone     string `json:"timezone"`
	BaseCurrency string `json:"base_currency"`
}

func LegalEntityOf(m *core.Base, ctx context.Context, id string) (LegalEntity, error) {
	c, err := Conn(m, core.Org)
	if err != nil {
		return LegalEntity{}, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_LEGAL_ENTITY, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return LegalEntity{}, err
	}
	var le LegalEntity
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return le, core.ErrLegalEntity.New("no legal entity " + id)
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &le); err != nil {
		return le, err
	}
	if le.State != "active" {
		return le, core.ErrLegalEntity.New("the legal entity is " + le.State)
	}
	return le, nil
}

// OrgUnit is a department as the projection keeps it (mdm.org_unit.changed.v1).
type OrgUnit struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Path          string `json:"path"`
	State         string `json:"state"`
}

// OrgUnitRead reads one department from mdm/org's source method, with its version and the source's identity.
func OrgUnitRead(m *core.Base, ctx context.Context, id string) (OrgUnit, int64, string, string, bool, error) {
	c, err := Conn(m, core.Org)
	if err != nil {
		return OrgUnit{}, 0, "", "", false, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_ORG_UNIT, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return OrgUnit{}, 0, "", "", false, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return OrgUnit{}, 0, res.GetStamp().GetInstance(), res.GetStamp().GetEpoch(), false, nil
	}
	var u OrgUnit
	r := res.GetResults()[0]
	return u, r.GetVersion(), res.GetStamp().GetInstance(), res.GetStamp().GetEpoch(), true, json.Unmarshal(r.GetData(), &u)
}

// Partner is a partner's snapshot for an open item or a supplier invoice.
type Partner struct {
	ID    string
	Name  string
	State string
}

// PartnerOf reads a partner from mdm/partner's source method; one that does not exist or is not active is refused.
func PartnerOf(m *core.Base, ctx context.Context, id string) (Partner, error) {
	c, err := Conn(m, core.Partner)
	if err != nil {
		return Partner{}, err
	}
	res, err := partnerv3.NewSourceServiceClient(c).Read(ctx, &partnerv3.ReadRequest{
		Aggregate: partnerv3.Aggregate_AGGREGATE_PARTNER, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return Partner{}, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != partnerv3.ReadState_READ_STATE_EXISTS {
		return Partner{}, core.ErrPartner.New("no partner " + id)
	}
	var p struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &p); err != nil {
		return Partner{}, err
	}
	if p.State != "active" {
		return Partner{}, core.ErrPartner.New("partner " + id + " is " + p.State)
	}
	return Partner{ID: id, Name: p.Name, State: p.State}, nil
}

// CurrencyScale is an active currency's minor unit.
func CurrencyScale(m *core.Base, ctx context.Context, code string) (int32, error) {
	c, err := Conn(m, core.Currency)
	if err != nil {
		return 0, err
	}
	res, err := currencyv3.NewSourceServiceClient(c).Read(ctx, &currencyv3.ReadRequest{
		Aggregate: currencyv3.Aggregate_AGGREGATE_CURRENCY, Ids: []string{code}}, calls.Idempotent())
	if err != nil {
		return 0, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != currencyv3.ReadState_READ_STATE_EXISTS {
		return 0, core.ErrCurrency.New("no currency " + code)
	}
	var cur struct {
		MinorUnit int32 `json:"minor_unit"`
		Active    bool  `json:"active"`
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &cur); err != nil {
		return 0, err
	}
	if !cur.Active {
		return 0, core.ErrCurrency.New("currency " + code + " is not active")
	}
	return cur.MinorUnit, nil
}

// PurchaseDeployed says whether erp/purchase is in the assembly (optional: without it an order invoice cannot be
// registered, SI02).
func PurchaseDeployed(m *core.Base) bool {
	_, ok := m.RT.GRPC(core.Purchase)
	return ok
}

// MatchBasis reads the order lines' match basis from erp/purchase (D50), keyed by order line; a line erp/purchase does
// not know is absent.
func MatchBasis(m *core.Base, ctx context.Context, lineIDs []string) (map[string]*purchasev3.BasisLine, error) {
	out := map[string]*purchasev3.BasisLine{}
	if len(lineIDs) == 0 {
		return out, nil
	}
	c, err := Conn(m, core.Purchase)
	if err != nil {
		return nil, err
	}
	res, err := purchasev3.NewMatchServiceClient(c).Basis(ctx, &purchasev3.BasisRequest{OrderLineIds: lineIDs}, calls.Idempotent())
	if err != nil {
		return nil, err
	}
	for _, l := range res.GetLines() {
		out[l.GetOrderLineId()] = l
	}
	return out, nil
}
