package ledger

import "sort"

// The posting rules (protocol 05's accounting fact contract): each rule key names the roles a fact carries amounts
// for and the side each posts to. Sources never name accounts; the legal entity maps every role to one of its
// accounts (account_mapping). The catalog is the component's: a new kind of fact is a new rule here.
var Rules = map[string]map[string]string{
	// an issued invoice (erp/billing): the receivable, the revenue and the output tax
	"sales_invoice": {"receivable": "debit", "revenue": "credit", "output_tax": "credit"},
	// a posted supplier invoice: the purchase (or expense), the input tax and the payable
	"supplier_invoice": {"purchase": "debit", "input_tax": "debit", "payable": "credit"},
	// money received (erp/payment's fund facts): against the customer's receivables, or held in suspense when the
	// payer is not known (ST02)
	"receipt": {"bank": "debit", "receivable": "credit", "suspense": "credit"},
	// money paid against payables
	"payment": {"payable": "debit", "bank": "credit"},
	// money paid back to a customer against its credit balance
	"refund": {"receivable": "debit", "bank": "credit"},
	// a manual open item approved (MI02): the receivable or the payable mapped, the counter account the one registered
	"manual_receivable": {"receivable": "debit", "counter": "credit"},
	"manual_payable":    {"counter": "debit", "payable": "credit"},
	// erp/payment's accounting facts (AF02): money moved between the company's accounts, a bank's charges, its interest
	// a transfer is a pair (protocol 05): going out, the money is in transit; arrived, in the other account
	"fund_transfer_out": {"in_transit": "debit", "bank": "credit"},
	"fund_transfer_in":  {"bank": "debit", "in_transit": "credit"},
	"bank_charge":       {"expense": "debit", "bank": "credit"},
	"bank_interest":     {"bank": "debit", "income": "credit"},
	// erp/costing's accounting facts (protocol 05b): a receipt valued (the goods received, not invoiced yet), the cost
	// of what went out and came back, a purchase return, and the adjustments when a receipt's price source is
	// replaced; the part of an adjustment whose goods went out already is the variance. Amounts are never negative,
	// so an adjustment up and one down are two rules.
	"inventory_receipt":    {"inventory": "debit", "grni": "credit"},
	"cost_of_sales":        {"cost_of_sales": "debit", "inventory": "credit"},
	"cost_of_sales_return": {"inventory": "debit", "cost_of_sales": "credit"},
	"inventory_return":     {"grni": "debit", "inventory": "credit"},
	"cost_adjustment_up":   {"inventory": "debit", "variance": "debit", "grni": "credit"},
	"cost_adjustment_down": {"grni": "debit", "inventory": "credit", "variance": "credit"},
}

// RuleKeys are the rule keys in order.
func RuleKeys() []string {
	keys := make([]string, 0, len(Rules))
	for k := range Rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
