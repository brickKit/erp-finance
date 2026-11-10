package core

import (
	"encoding/json"
	"fmt"

	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Base is what every module works with: the component's runtime (its database, calls, events, audit, logger) and
// the configuration read once at the start.
type Base struct {
	RT *runtime.Runtime
	// Tolerances are the supplier invoice matching's tolerances by legal entity (KeyMatchTolerances)
	Tolerances map[string]Tolerance
}

// Tolerance is a legal entity's matching tolerance in percent (SI02).
type Tolerance struct {
	QuantityPercent string `json:"quantity_percent"`
	PricePercent    string `json:"price_percent"`
}

// KeyMatchTolerances is the configuration key of the tolerances.
const KeyMatchTolerances = "MATCH_TOLERANCES"

// NewBase reads the configuration a module needs; a malformed value stops the start.
func NewBase(rt *runtime.Runtime) (*Base, error) {
	b := &Base{RT: rt, Tolerances: map[string]Tolerance{}}
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
	return b, nil
}

// ToleranceOf is a legal entity's tolerance; 0 and 0 when it declares none.
func (b *Base) ToleranceOf(legalEntity string) Tolerance {
	if t, ok := b.Tolerances[legalEntity]; ok {
		return t
	}
	return Tolerance{QuantityPercent: "0", PricePercent: "0"}
}
