package core

import (
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// ID is the component's ID.
const ID = "erp/finance"

// Dependencies (the IDs runtime.GRPC takes) and callers.
const (
	Partner  = "mdm/partner"
	Org      = "mdm/org"
	Currency = "mdm/currency"
	// Purchase answers the match basis of supplier invoices (D50); optional.
	Purchase = "erp/purchase"
	// Billing publishes the issued invoices receivables are formed from (only through events).
	Billing = "erp/billing"
	// Payment claims open items before it pays (protocol 05's payment claims).
	Payment = "erp/payment"
)

// The stream and its event types (contract/events).
const (
	Stream             = "erp.finance"
	VoucherPosted      = "erp.voucher.posted.v1"
	ReceivableFormed   = "erp.receivable.formed.v1"
	OpenItemSettled    = "erp.open_item.settled.v1"
	ReceivableCleared  = "erp.receivable.cleared.v1"
	ReceivableReduced  = "erp.receivable.reduced.v1"
	ReceivableReopened = "erp.receivable.reopened.v1"
	PriceConfirmed     = "erp.supplier_invoice.price_confirmed.v1"
	PriceReversed      = "erp.supplier_invoice.price_reversed.v1"
	PeriodChanged      = "erp.accounting_period.changed.v1"
	// the source stream of the department projection
	OrgStream       = "mdm.org"
	OrgUnitChanged  = "mdm.org_unit.changed.v1"
	OrgUnitEvents   = "mdm.org_unit.>" // every department event, later actions and majors too (protocol P6.33)
	OrgUnitsProject = "org_units"
	// the issued invoices (erp/billing)
	BillingStream = "erp.billing"
	InvoiceIssued = "erp.invoice.issued.v1"
)

// Resources and operations (contract/openapi.yaml names each route's operation).
const (
	Accounts             = "erp.finance.account"
	OpAccountRead        = "erp.finance.account.read"
	OpAccountManage      = "erp.finance.account.manage"
	Vouchers             = "erp.finance.voucher"
	OpVoucherRead        = "erp.finance.voucher.read"
	OpVoucherEdit        = "erp.finance.voucher.edit"
	OpVoucherApprove     = "erp.finance.voucher.approve"
	Periods              = "erp.finance.period"
	OpPeriodRead         = "erp.finance.period.read"
	OpPeriodClose        = "erp.finance.period.close"
	OpenItems            = "erp.finance.open_item"
	OpOpenItemRead       = "erp.finance.open_item.read"
	OpOpenItemEdit       = "erp.finance.open_item.edit"
	OpOpenItemApprove    = "erp.finance.open_item.approve"
	SupplierInvoices     = "erp.finance.supplier_invoice"
	OpSupplierInvRead    = "erp.finance.supplier_invoice.read"
	OpSupplierInvEdit    = "erp.finance.supplier_invoice.edit"
	OpSupplierInvApprove = "erp.finance.supplier_invoice.approve"
	System               = "erp.finance.system"
	OpSysRead            = "erp.finance.system.read"
	OpSysClaim           = "erp.finance.system.claim"
	// OpResend is the redelivery of this component's stream (be-protocol P6.38).
	OpResend = "erp.finance.system.resend"
	// OpPostFact is the audit's operation of a system voucher posted from a received fact; no one calls it, so it is
	// no operation of the definitions (the actor is the fact's source).
	OpPostFact = "erp.finance.voucher.post_fact"
	// OpRecover is a full recovery's checks (be-protocol chapter 14: the recovery tool only).
	OpRecover = "erp.finance.system.recover"
)

// Dimensions (security 07): the legal entity and, for open items and supplier invoices, the department.
const (
	DimLegalEntity = "legal_entity"
	DimDepartment  = "department"
)

// Parameters (protocol 05, protocol 07, protocol 14).
const (
	ClaimPeriod   = 7 * 24 * time.Hour // a pending payment claim's period (protocol 05's start values)
	MaxReadIDs    = 500
	MaxLines      = 500
	PriceScale    = 6
	QuantityScale = 6
)

// DepartmentTables are the tables that copy their department's path (internal/orgunits rewrites them when a
// department moves).
var DepartmentTables = []string{"open_item"}

func fields(names ...string) map[string]sdkauthz.Field {
	out := map[string]sdkauthz.Field{}
	for _, n := range names {
		out[n] = sdkauthz.Field{Class: sdkauthz.Ordinary}
	}
	return out
}

// AccountFields are what an account list may filter and sort on.
var AccountFields = []string{"id", "legal_entity_id", "code", "name", "category", "parent_id", "postable", "state",
	"version", "updated_at"}

// VoucherFields are what a voucher list may filter and sort on.
var VoucherFields = []string{"id", "legal_entity_id", "number", "period", "business_date", "posting_date", "description",
	"source", "state", "maker_id", "approver_id", "reason", "reverses_voucher_id", "reversed_by_id", "lines", "version",
	"created_at", "updated_at"}

// OpenItemFields are what an open item list may filter and sort on.
var OpenItemFields = []string{"id", "legal_entity_id", "kind", "partner_id", "currency", "amount", "settled", "pay_claimed",
	"available", "open_balance", "open", "due_date", "origin", "source", "state", "department_id", "claims", "allocations",
	"version", "created_at", "updated_at"}

// PeriodFields are what a period list may filter and sort on.
var PeriodFields = []string{"legal_entity_id", "period", "state", "revision", "reason", "version", "updated_at"}

func entity() map[string]sdkauthz.Dimension {
	return map[string]sdkauthz.Dimension{DimLegalEntity: {Kind: sdkauthz.ValueSet, Column: "legal_entity_id"}}
}

func entityAndDepartment() map[string]sdkauthz.Dimension {
	return map[string]sdkauthz.Dimension{DimLegalEntity: {Kind: sdkauthz.ValueSet, Column: "legal_entity_id"},
		DimDepartment: {Kind: sdkauthz.OrgTree, Column: "department_path"}}
}

// Definitions are the component's resources and operations.
func Definitions() *sdkauthz.Definitions {
	low := func(key, typ, purpose string, audit bool) sdkauthz.Operation {
		op := sdkauthz.Operation{Key: key, ResourceType: typ, Purpose: purpose, RiskClass: "low", Audit: audit}
		if purpose != "read" {
			op.SideEffects = []string{"business_write"}
		}
		return op
	}
	high := func(key, typ, purpose string) sdkauthz.Operation {
		return sdkauthz.Operation{Key: key, ResourceType: typ, Purpose: purpose, RiskClass: "high", Audit: true,
			SideEffects: []string{"business_write"}}
	}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: Accounts, ViewOperation: OpAccountRead, Dimensions: entity(), Fields: fields(AccountFields...)},
			{Type: Vouchers, ViewOperation: OpVoucherRead, Dimensions: entity(), Fields: fields(VoucherFields...)},
			{Type: Periods, ViewOperation: OpPeriodRead, Dimensions: entity(), Fields: fields(PeriodFields...)},
			{Type: OpenItems, ViewOperation: OpOpenItemRead, Dimensions: entityAndDepartment(), Fields: fields(OpenItemFields...)},
			{Type: SupplierInvoices, ViewOperation: OpSupplierInvRead, Dimensions: entityAndDepartment(), Fields: fields("id")},
			{Type: System, ViewOperation: OpSysRead, Dimensions: map[string]sdkauthz.Dimension{},
				Fields: map[string]sdkauthz.Field{}},
		},
		Operations: []sdkauthz.Operation{
			low(OpAccountRead, Accounts, "read", false),
			high(OpAccountManage, Accounts, "manage"),
			low(OpVoucherRead, Vouchers, "read", false),
			low(OpVoucherEdit, Vouchers, "manage", true),
			high(OpVoucherApprove, Vouchers, "action"),
			low(OpPeriodRead, Periods, "read", false),
			high(OpPeriodClose, Periods, "action"),
			low(OpOpenItemRead, OpenItems, "read", false),
			low(OpOpenItemEdit, OpenItems, "manage", true),
			high(OpOpenItemApprove, OpenItems, "action"),
			low(OpSupplierInvRead, SupplierInvoices, "read", false),
			low(OpSupplierInvEdit, SupplierInvoices, "manage", true),
			high(OpSupplierInvApprove, SupplierInvoices, "action"),
			low(OpSysRead, System, "read", false),
			{Key: OpSysClaim, ResourceType: System, Purpose: "action", RiskClass: "low", SideEffects: []string{"business_write"}},
			{Key: OpResend, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpRecover, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}
