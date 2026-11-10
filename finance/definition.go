// Package finance is the finance component: one ledger per legal entity (D51) with its chart of accounts, posting
// rules, periods and vouchers; receivables from issued invoices, payables from supplier invoices matched against
// purchase orders (D50), the payment claims erp/payment makes, and the settlement of its fund facts (protocol 05); the
// shared accounting fact contract the sources publish under.
package finance

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity.
const (
	Version  = "3.0.0"
	GRPCPort = 9097
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Keys are the component's own configuration keys.
func Keys() []config.Key {
	return []config.Key{
		// the supplier invoice matching's tolerances per legal entity (SI02): JSON, legal entity ID -> {"quantity_percent",
		// "price_percent"} as decimal strings; a legal entity not named has 0 and 0. Unset: every legal entity 0 and 0
		{Name: core.KeyMatchTolerances, Type: config.String},
	}
}

// Own describes the own keys for the manifest's configSchema.
var Own = map[string]string{core.KeyMatchTolerances: "the supplier invoice matching's tolerances per legal entity " +
	"(JSON: legal entity ID -> {quantity_percent, price_percent}, decimal strings); a legal entity not named has 0"}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Keys: Keys(), Migrations: migrations(), GRPCPort: GRPCPort, Subscribes: true,
		Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(), Resend: core.OpResend,
		Recovery: core.OpRecover, New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
