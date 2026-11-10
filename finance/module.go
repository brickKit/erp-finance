package finance

import (
	"context"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	orgunits "github.com/brickKit/erp-finance/v3/internal/orgunits"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"
	vouchers "github.com/brickKit/erp-finance/v3/internal/vouchers"

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
	h := func(f func(*core.Base, *httpx.Context) error) httpx.Handler {
		return func(c *httpx.Context) error { return f(m.b, c) }
	}
	todo := core.Unsupported("this route")
	add("GET", "/api/finance/accounts", core.OpAccountRead, h(ledger.ListAccounts))                                              // listAccounts
	add("POST", "/api/finance/accounts", core.OpAccountManage, h(ledger.CreateAccount))                                          // createAccount
	add("GET", "/api/finance/accounts/:id", core.OpAccountRead, h(ledger.GetAccount))                                            // getAccount
	add("PATCH", "/api/finance/accounts/:id", core.OpAccountManage, h(ledger.UpdateAccount))                                     // updateAccount
	add("GET", "/api/finance/posting-rules", core.OpAccountRead, h(ledger.ListPostingRules))                                     // listPostingRules
	add("GET", "/api/finance/account-mappings/:legal_entity_id", core.OpAccountRead, h(ledger.GetMapping))                       // getMapping
	add("PUT", "/api/finance/account-mappings/:legal_entity_id", core.OpAccountManage, h(ledger.PutMapping))                     // putMapping
	add("GET", "/api/finance/periods", core.OpPeriodRead, h(periods.ListPeriods))                                                // listPeriods
	add("POST", "/api/finance/periods/:legal_entity_id/:period/close", core.OpPeriodClose, periods.Action(m.b, periods.Close))   // closePeriod
	add("POST", "/api/finance/periods/:legal_entity_id/:period/reopen", core.OpPeriodClose, periods.Action(m.b, periods.Reopen)) // reopenPeriod
	add("GET", "/api/finance/vouchers", core.OpVoucherRead, h(vouchers.ListVouchers))                                            // listVouchers
	add("POST", "/api/finance/vouchers", core.OpVoucherEdit, h(vouchers.CreateVoucher))                                          // createVoucher
	add("GET", "/api/finance/vouchers/:id", core.OpVoucherRead, h(vouchers.GetVoucher))                                          // getVoucher
	add("PATCH", "/api/finance/vouchers/:id", core.OpVoucherEdit, h(vouchers.UpdateVoucher))                                     // updateVoucher
	add("POST", "/api/finance/vouchers/:id/submit", core.OpVoucherEdit, vouchers.Action(m.b, vouchers.Submit))                   // submitVoucher
	add("POST", "/api/finance/vouchers/:id/approve", core.OpVoucherApprove, vouchers.Action(m.b, vouchers.Approve))              // approveVoucher
	add("POST", "/api/finance/vouchers/:id/reject", core.OpVoucherApprove, vouchers.Action(m.b, vouchers.Reject))                // rejectVoucher
	add("POST", "/api/finance/vouchers/:id/reverse", core.OpVoucherApprove, vouchers.Action(m.b, vouchers.Reverse))              // reverseVoucher
	add("GET", "/api/finance/balances", core.OpVoucherRead, h(ledger.ListBalances))                                              // listBalances
	add("GET", "/api/finance/open-items", core.OpOpenItemRead, todo)                                                             // listOpenItems
	add("POST", "/api/finance/open-items", core.OpOpenItemEdit, todo)                                                            // createOpenItem
	add("GET", "/api/finance/open-items/:id", core.OpOpenItemRead, todo)                                                         // getOpenItem
	add("POST", "/api/finance/open-items/:id/approve", core.OpOpenItemApprove, todo)                                             // approveOpenItem
	add("POST", "/api/finance/open-items/:id/cancel", core.OpOpenItemEdit, todo)                                                 // cancelOpenItem
	add("GET", "/api/finance/supplier-invoices", core.OpSupplierInvRead, todo)                                                   // listSupplierInvoices
	add("POST", "/api/finance/supplier-invoices", core.OpSupplierInvEdit, todo)                                                  // createSupplierInvoice
	add("GET", "/api/finance/supplier-invoices/:id", core.OpSupplierInvRead, todo)                                               // getSupplierInvoice
	add("PATCH", "/api/finance/supplier-invoices/:id", core.OpSupplierInvEdit, todo)                                             // updateSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/match", core.OpSupplierInvEdit, todo)                                        // matchSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/approve-difference", core.OpSupplierInvApprove, todo)                        // approveDifference
	add("POST", "/api/finance/supplier-invoices/:id/post", core.OpSupplierInvEdit, todo)                                         // postSupplierInvoice
	add("POST", "/api/finance/supplier-invoices/:id/cancel", core.OpSupplierInvEdit, todo)                                       // cancelSupplierInvoice
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
