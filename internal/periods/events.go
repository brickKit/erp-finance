package periods

import (
	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/events"
)

// eventOf is the period's full-state event; its record is <legal entity>/<month>.
func eventOf(p Period) events.Event {
	return events.Event{Type: core.PeriodChanged, RecordID: p.LegalEntityID + "/" + p.Period, Version: p.Version,
		Projection: events.Full, LegalEntity: p.LegalEntityID, Data: p}
}
