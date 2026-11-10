package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Base is what every module works with: the component's runtime (its database, calls, events, audit, logger) and
// the configuration read once at the start.
type Base struct {
	RT *runtime.Runtime
	// Tolerances are the supplier invoice matching's tolerances by legal entity (KeyMatchTolerances)
	Tolerances map[string]Tolerance
	// ClaimPeriod is a pending payment claim's period (KeyClaimPeriod, CL01)
	ClaimPeriod time.Duration
	// FactSources are the streams and event types of the accounting and fund facts subscribed to (KeyFactSources, ST01)
	FactSources []FactSource
}

// FactSource is a source's stream and the event type of its accounting or fund facts there.
type FactSource struct {
	Stream string `json:"stream"`
	Type   string `json:"type"`
}

// The suffixes of the shared fact contract's event types (contract/events/shared).
const (
	FundFactSuffix       = ".fund_fact.v1"
	AccountingFactSuffix = ".accounting_fact.v1"
)

// Tolerance is a legal entity's matching tolerance in percent (SI02).
type Tolerance struct {
	QuantityPercent string `json:"quantity_percent"`
	PricePercent    string `json:"price_percent"`
}

// The configuration keys of the component (finance/definition.go declares them).
const (
	KeyMatchTolerances = "MATCH_TOLERANCES"
	KeyClaimPeriod     = "CLAIM_PERIOD"
	KeyFactSources     = "FINANCE_FACT_SOURCES"
)

// NewBase reads the configuration a module needs; a malformed value stops the start.
func NewBase(rt *runtime.Runtime) (*Base, error) {
	b := &Base{RT: rt, Tolerances: map[string]Tolerance{}, ClaimPeriod: DefaultClaimPeriod}
	if rt.Config.Has(KeyClaimPeriod) {
		if b.ClaimPeriod = rt.Config.Duration(KeyClaimPeriod); b.ClaimPeriod <= 0 {
			return nil, fmt.Errorf("%s: a claim's period is positive", KeyClaimPeriod)
		}
	}
	if raw := rt.Config.String(KeyMatchTolerances); raw != "" {
		if err := json.Unmarshal([]byte(raw), &b.Tolerances); err != nil {
			return nil, fmt.Errorf("%s: %w", KeyMatchTolerances, err)
		}
		for le, t := range b.Tolerances {
			if !UUIDForm.MatchString(le) || !DecimalForm.MatchString(t.QuantityPercent) || !DecimalForm.MatchString(t.PricePercent) {
				return nil, fmt.Errorf("%s: %s is not a legal entity ID with two decimal percentages", KeyMatchTolerances, le)
			}
		}
	}
	if raw := rt.Config.String(KeyFactSources); raw != "" {
		if err := json.Unmarshal([]byte(raw), &b.FactSources); err != nil {
			return nil, fmt.Errorf("%s: %w", KeyFactSources, err)
		}
		seen := map[FactSource]bool{}
		for _, s := range b.FactSources {
			if !streamForm.MatchString(s.Stream) || seen[s] ||
				!(strings.HasSuffix(s.Type, FundFactSuffix) || strings.HasSuffix(s.Type, AccountingFactSuffix)) {
				return nil, fmt.Errorf("%s: %+v is not a stream with a fund or accounting fact type, once", KeyFactSources, s)
			}
			seen[s] = true
		}
	}
	return b, nil
}

var streamForm = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ToleranceOf is a legal entity's tolerance; 0 and 0 when it declares none.
func (b *Base) ToleranceOf(legalEntity string) Tolerance {
	if t, ok := b.Tolerances[legalEntity]; ok {
		return t
	}
	return Tolerance{QuantityPercent: "0", PricePercent: "0"}
}

// Received is a received fact as its posting takes it (internal/intake gathers it before the posting transaction):
// the fact, its producer, and the evidence of its legal entity.
type Received struct {
	FactID string
	Type   string
	// Source is the producing component (the audit's actor of what the fact posts)
	Source string
	Data   []byte
	// LegalEntityID, LegalEntityCode and BaseCurrency are the legal entity's; BaseScale its currency's places
	LegalEntityID   string
	LegalEntityCode string
	BaseCurrency    string
	BaseScale       int32
}

// ErrWaitForOriginal says a fact waits for another not posted yet (a reversal before its fund fact, ST05): its
// posting retries without spending its attempts.
var ErrWaitForOriginal = fmt.Errorf("the fact waits for the fact it corrects")
