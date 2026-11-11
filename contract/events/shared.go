// Package events carries erp/finance's shared contracts to the source components that publish them (protocol 05):
// a source declares its own event type with a schema that refers to one of these by its $id, and its tests load
// Shared to check what it publishes. The files are the contract; nothing is copied.
package events

import "embed"

// Shared holds shared/accounting_fact.v1.schema.json and shared/fund_fact.v1.schema.json.
//
//go:embed shared/*.json
var Shared embed.FS
