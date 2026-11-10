//go:build integration

package itest

// The stand-ins of the integration test: the master data components, each speaking its published contract over mTLS.

import (
	"context"
	"encoding/json"
	"sync"

	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
)

// --- mdm/org: legal entities and departments ---

type orgStandIn struct {
	orgv3.UnimplementedSourceServiceServer
	mu    sync.Mutex
	les   map[string]map[string]any
	units map[string]map[string]any
}

var orgStamp = &orgv3.Stamp{Instance: "org-it", Epoch: "1.1"}

func (o *orgStandIn) Read(_ context.Context, in *orgv3.ReadRequest) (*orgv3.ReadResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	src := o.les
	if in.GetAggregate() == orgv3.Aggregate_AGGREGATE_ORG_UNIT {
		src = o.units
	}
	out := &orgv3.ReadResponse{Stamp: orgStamp}
	for _, id := range in.GetIds() {
		r := &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_PURGED}
		if v, ok := src[id]; ok {
			data, _ := json.Marshal(v)
			r = &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_EXISTS, Version: v["version"].(int64), Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

func (o *orgStandIn) Snapshot(_ context.Context, in *orgv3.SnapshotRequest) (*orgv3.SnapshotResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &orgv3.SnapshotResponse{Stamp: orgStamp}
	if in.GetAggregate() != orgv3.Aggregate_AGGREGATE_ORG_UNIT || in.GetCursor() != "" {
		return out, nil
	}
	for id, v := range o.units {
		data, _ := json.Marshal(v)
		out.Records = append(out.Records, &orgv3.Record{Id: id, Version: v["version"].(int64), Data: data})
	}
	return out, nil
}

// --- mdm/partner ---

type partnerStandIn struct {
	partnerv3.UnimplementedSourceServiceServer
	partnerv3.UnimplementedPartnerServiceServer
	names   map[string]string
	blocked map[string]bool
}

func (p *partnerStandIn) CheckEligibility(_ context.Context, in *partnerv3.CheckEligibilityRequest) (*partnerv3.CheckEligibilityResponse, error) {
	if _, ok := p.names[in.GetPartnerId()]; !ok {
		return &partnerv3.CheckEligibilityResponse{Reason: "not_found"}, nil
	}
	if p.blocked[in.GetPartnerId()] {
		return &partnerv3.CheckEligibilityResponse{Reason: "partner_suspended"}, nil
	}
	return &partnerv3.CheckEligibilityResponse{Eligible: true, Reason: "eligible"}, nil
}

func (p *partnerStandIn) Read(_ context.Context, in *partnerv3.ReadRequest) (*partnerv3.ReadResponse, error) {
	out := &partnerv3.ReadResponse{Stamp: &partnerv3.Stamp{}}
	for _, id := range in.GetIds() {
		r := &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_PURGED}
		if n, ok := p.names[id]; ok {
			state := "active"
			if p.blocked[id] {
				state = "suspended"
			}
			data, _ := json.Marshal(map[string]any{"id": id, "name": n, "tax_id": "91310000-" + id[:4], "state": state})
			r = &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

// --- mdm/currency: CNY and USD ---

type currencyStandIn struct {
	currencyv3.UnimplementedSourceServiceServer
}

func (currencyStandIn) Read(_ context.Context, in *currencyv3.ReadRequest) (*currencyv3.ReadResponse, error) {
	out := &currencyv3.ReadResponse{Stamp: &currencyv3.Stamp{}}
	for _, code := range in.GetIds() {
		r := &currencyv3.ReadResult{Id: code, State: currencyv3.ReadState_READ_STATE_PURGED}
		if code == "CNY" || code == "USD" {
			data, _ := json.Marshal(map[string]any{"code": code, "minor_unit": 2, "active": true})
			r = &currencyv3.ReadResult{Id: code, State: currencyv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}
