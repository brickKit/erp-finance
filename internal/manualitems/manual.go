package manualitems

import (
	"context"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	openitems "github.com/brickKit/erp-finance/v3/internal/openitems"
	orgunits "github.com/brickKit/erp-finance/v3/internal/orgunits"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
)

type itemIn struct {
	LegalEntityID      string  `json:"legal_entity_id"`
	Kind               string  `json:"kind"`
	PartnerID          string  `json:"partner_id"`
	Currency           string  `json:"currency"`
	Amount             string  `json:"amount"`
	DueDate            string  `json:"due_date"`
	Description        string  `json:"description"`
	CounterAccountCode string  `json:"counter_account_code"`
	DepartmentID       *string `json:"department_id"`
}

// CreateOpenItem registers a manual open item waiting for approval (MI01); its maker is the one who registers it.
func CreateOpenItem(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in itemIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case in.Kind != "receivable" && in.Kind != "payable":
		return core.Invalid("kind", "kind is receivable or payable")
	case !core.UUIDForm.MatchString(in.PartnerID):
		return core.Invalid("partner_id", "partner_id is a partner's ID")
	case !core.DateForm.MatchString(in.DueDate):
		return core.Invalid("due_date", "due_date is YYYY-MM-DD")
	case strings.TrimSpace(in.Description) == "":
		return core.Invalid("description", "description is required")
	case strings.TrimSpace(in.CounterAccountCode) == "":
		return core.Invalid("counter_account_code", "counter_account_code is required")
	}
	le, err := masterdata.LegalEntityOf(m, c.Ctx(), in.LegalEntityID)
	if err != nil {
		return err
	}
	if in.Currency != le.BaseCurrency {
		return core.ErrCurrency.New("a manual open item is in the legal entity's currency " + le.BaseCurrency + " (foreign currency later)")
	}
	scale, err := masterdata.CurrencyScale(m, c.Ctx(), in.Currency)
	if err != nil {
		return err
	}
	amount, err := core.Decimal("amount", in.Amount, scale, true)
	if err != nil {
		return err
	}
	if _, err := masterdata.PartnerOf(m, c.Ctx(), in.PartnerID); err != nil {
		return err
	}
	deptID, deptPath, err := orgunits.Usable(m, c.Ctx(), in.DepartmentID, in.LegalEntityID)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpOpenItemEdit, core.DocFacts(in.LegalEntityID, deptPath))
	if err != nil {
		return err
	}
	today, err := bizdate.Today(le.Timezone)
	if err != nil {
		return err
	}
	number, err := core.Number(m, ctx, "MI", in.LegalEntityID, le.Code, today)
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		if err := ledger.Postable(ctx, tx, in.LegalEntityID, in.CounterAccountCode); err != nil {
			return idem.Outcome{}, err
		}
		o, err := openitems.Form(m, ctx, tx, openitems.New{LegalEntityID: in.LegalEntityID, Kind: in.Kind,
			PartnerID: in.PartnerID, Currency: in.Currency, Amount: amount, DueDate: in.DueDate, Origin: "manual",
			Source: openitems.Source{Kind: "manual", Number: &number}, DepartmentID: deptID, DepartmentPath: deptPath,
			State: PendingApproval, MakerID: &actor, Description: &in.Description, CounterAccountCode: &in.CounterAccountCode})
		if err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(o.ID), core.Change(m, ctx, tx, actor, core.OpOpenItemEdit, core.OpenItems, o.ID, o)
	})
	if err != nil {
		return err
	}
	o, err := openitems.Read(m, ctx, core.ResultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, o)
}

type actionIn struct {
	Version *int64 `json:"version"`
}

// Action is approve or cancel as the table decides it in the item's state (MI02, MI03); an ignored command answers
// the item as it is. Only a manual open item is acted on.
func Action(m *core.Base, command string) httpx.Handler {
	op := core.OpOpenItemEdit
	if command == Approve {
		op = core.OpOpenItemApprove
	}
	return func(c *httpx.Context) error {
		var in actionIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		cur, err := openitems.Read(m, c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, op, core.DocFacts(cur.LegalEntityID, cur.DepartmentPath))
		if err != nil {
			return err
		}
		if cur.Origin != "manual" {
			return core.ErrState.New("only a manual open item is approved or cancelled; this one comes from its source")
		}
		le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
		if err != nil {
			return err
		}
		today, err := bizdate.Today(le.Timezone)
		if err != nil {
			return err
		}
		scale, err := masterdata.CurrencyScale(m, ctx, cur.Currency)
		if err != nil {
			return err
		}
		var out openitems.OpenItem
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			o, err := openitems.Get(ctx, tx, cur.ID, true)
			if err != nil {
				return err
			}
			out = o
			state := o.State
			if state == "settled" { // the read state; the stored one is open
				state = Open
			}
			d := Lifecycle.Decide(state, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, state)
			}
			if in.Version != nil && *in.Version != o.Version {
				return core.ErrVersion.New(fmt.Sprintf("the open item is at version %d", o.Version))
			}
			var maker, counter string
			if err := tx.QueryRow(ctx, `SELECT coalesce(maker_id, ''), coalesce(counter_account_code, '') FROM open_item
				WHERE id = $1`, o.ID).Scan(&maker, &counter); err != nil {
				return err
			}
			voucher := ""
			if command == Approve {
				if maker == actor { // GuardNotMaker
					return core.ErrApprover.New("the open item was registered by " + actor + ": another approves it")
				}
				if voucher, err = post(m, ctx, tx, o, le, scale, counter, today, actor, d.Op); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE open_item SET state = $2, voucher_id = nullif($3, '')::uuid,
				version = version + 1, updated_at = now() WHERE id = $1`, o.ID, d.To, voucher); err != nil {
				return err
			}
			if out, err = openitems.Get(ctx, tx, o.ID, false); err != nil {
				return err
			}
			if command == Approve {
				if err := openitems.Formed(m, ctx, tx, out, nil); err != nil {
					return err
				}
			}
			return core.Change(m, ctx, tx, actor, d.Op, core.OpenItems, o.ID, out)
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// post is MI02's voucher: the receivable or the payable on its mapped account, the counter account the one registered,
// dated today and postponed past a closed month (PD01).
func post(m *core.Base, ctx context.Context, tx db.Tx, o openitems.OpenItem, le masterdata.LegalEntity, scale int32,
	counter, today, actor, op string) (string, error) {
	rule := "manual_" + o.Kind
	lines, err := ledger.Resolve(ctx, tx, o.LegalEntityID, rule, map[string]string{o.Kind: o.Amount})
	if err != nil {
		return "", err
	}
	lines = append(lines, ledger.Line{AccountCode: counter, Side: ledger.Rules[rule]["counter"], Amount: o.Amount})
	for i := range lines {
		lines[i].PartnerID, lines[i].DepartmentID = &o.PartnerID, o.DepartmentID
	}
	date, err := periods.PostingDate(m, ctx, tx, o.LegalEntityID, today)
	if err != nil {
		return "", err
	}
	id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: o.LegalEntityID, BusinessDate: today, PostingDate: date,
		Description: "人工未清项 " + deref(o.Source.Number), Source: ledger.Source{Kind: "manual_open_item", ID: &o.ID},
		State: "posted", Lines: ledger.Convert(lines, o.Currency, nil, scale)})
	if err != nil {
		return "", err
	}
	_, err = ledger.Issue(m, ctx, tx, id, le.Code, actor, op, &actor)
	return id, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
