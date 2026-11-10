package finance

import (
	"context"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	orgunits "github.com/brickKit/erp-finance/v3/internal/orgunits"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// Module is the finance component as the launcher and the shells run it: it only assembles the business modules
// under internal/ (their map is internal/AGENTS.md).
type Module struct {
	b *core.Base
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) {
	return &Module{b: &core.Base{RT: rt}}, nil
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h})
	}
	todo := core.Unsupported("this route")
	add("GET", "/api/finance/accounts", core.OpAccountRead, todo)                                         // listAccounts
	add("POST", "/api/finance/accounts", core.OpAccountManage, todo)                                      // createAccount
	add("GET", "/api/finance/accounts/:id", core.OpAccountRead, todo)                                     // getAccount
	add("PATCH", "/api/finance/accounts/:id", core.OpAccountManage, todo)                                 // updateAccount
	add("GET", "/api/finance/posting-rules", core.OpAccountRead, todo)                                    // listPostingRules
	add("GET", "/api/finance/account-mappings/:legal_entity_id", core.OpAccountRead, todo)                // getMapping
	add("PUT", "/api/finance/account-mappings/:legal_entity_id", core.OpAccountManage, todo)              // putMapping
	add("GET", "/api/finance/periods", core.OpPeriodRead, todo)                                           // listPeriods
	add("POST", "/api/finance/periods/:legal_entity_id/:period/close", core.OpPeriodClose, todo)          // closePeriod
	add("POST", "/api/finance/periods/:legal_entity_id/:period/reopen", core.OpPeriodClose, todo)         // reopenPeriod
	add("GET", "/api/finance/vouchers", core.OpVoucherRead, todo)                                         // listVouchers
	add("POST", "/api/finance/vouchers", core.OpVoucherEdit, todo)                                        // createVoucher
	add("GET", "/api/finance/vouchers/:id", core.OpVoucherRead, todo)                                     // getVoucher
	add("PATCH", "/api/finance/vouchers/:id", core.OpVoucherEdit, todo)                                   // updateVoucher
	add("POST", "/api/finance/vouchers/:id/submit", core.OpVoucherEdit, todo)                             // submitVoucher
	add("POST", "/api/finance/vouchers/:id/approve", core.OpVoucherApprove, todo)                         // approveVoucher
	add("POST", "/api/finance/vouchers/:id/reject", core.OpVoucherApprove, todo)                          // rejectVoucher
	add("POST", "/api/finance/vouchers/:id/reverse", core.OpVoucherApprove, todo)                         // reverseVoucher
	add("GET", "/api/finance/balances", core.OpVoucherRead, todo)                                         // listBalances
	add("GET", "/api/finance/open-items", core.OpOpenItemRead, todo)                                      // listOpenItems
	add("POST", "/api/finance/open-items", core.OpOpenItemEdit, todo)                                     // createOpenItem
	add("GET", "/api/finance/open-items/:id", core.OpOpenItemRead, todo)                                  // getOpenItem
	add("POST", "/api/finance/open-items/:id/approve", core.OpOpenItemApprove, todo)                      // approveOpenItem
	add("POST", "/api/finance/open-items/:id/cancel", core.OpOpenItemEdit, todo)                          // cancelOpenItem
	add("GET", "/api/finance/supplier-invoices", core.OpSupplierInvRead, todo)                            // listSupplierInvoices
	add("POST", "/api/finance/supplier-invoices", core.OpSupplierInvEdit, todo)                           // createSupplierInvoice
	add("GET", "/api/finance/supplier-invoices/:id", core.OpSupplierInvRead, todo)                        // getSupplierInvoice
	add("PATCH", "/api/finance/supplier-invoices/:id", core.OpSupplierInvEdit, todo)                      // updateSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/match", core.OpSupplierInvEdit, todo)                 // matchSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/approve-difference", core.OpSupplierInvApprove, todo) // approveDifference
	add("POST", "/api/finance/supplier-invoices/:id/post", core.OpSupplierInvEdit, todo)                  // postSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/cancel", core.OpSupplierInvEdit, todo)                // cancelSupplierInvoice
	// in the contract, not in this version (D75; docs/design.md, "实现状态")
	add("POST", "/api/finance/closings", core.OpPeriodClose, core.Unsupported("a closing batch"))
	add("POST", "/api/finance/revaluations", core.OpPeriodClose, core.Unsupported("foreign currency revaluation"))
	add("GET", "/api/finance/release-ledgers/:order_id", core.OpOpenItemRead, core.Unsupported("an order's release ledger"))
	return nil
}

// Subscriptions declares the department projection's input (protocol 02).
func (m *Module) Subscriptions(s *events.Subscriptions) error {
	s.Add(events.Sub{Name: "org-unit-events", Streams: []string{core.OrgStream}, Types: []string{core.OrgUnitEvents},
		Handle: m.received(orgunits.OnOrgUnit)})
	return nil
}

// Jobs declares the background work (protocol 01).
func (m *Module) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: "align-org-units", Kind: work.Singleton, Effect: work.Local, Interval: time.Hour,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.AlignOrgUnits)})
	j.Add(work.Job{Name: orgunits.JobRepairOrgUnits, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.RepairOrgUnits)})
	j.Add(work.Job{Name: orgunits.JobRealignOrgUnits, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.RealignOrgUnits)})
	return nil
}

// job and received adapt a module's function, which takes the shared base first, to what the SDK registers.
func (m *Module) job(f func(*core.Base, context.Context, *work.Run) error) func(context.Context, *work.Run) error {
	return func(ctx context.Context, r *work.Run) error { return f(m.b, ctx, r) }
}

func (m *Module) received(f func(*core.Base, context.Context, db.Tx, events.Received) error) func(context.Context, db.Tx, events.Received) error {
	return func(ctx context.Context, tx db.Tx, e events.Received) error { return f(m.b, ctx, tx, e) }
}
