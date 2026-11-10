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
