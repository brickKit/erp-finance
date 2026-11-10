package vouchers

import (
	"context"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	ledger "github.com/brickKit/erp-finance/v3/internal/ledger"
	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	periods "github.com/brickKit/erp-finance/v3/internal/periods"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/list"
)

// Manual vouchers: drafts in the legal entity's base currency, their change, submission, approval (posting) or
// rejection, and reversal.

type lineIn struct {
	AccountCode  string  `json:"account_code"`
	Side         string  `json:"side"`
	Amount       string  `json:"amount"`
	PartnerID    *string `json:"partner_id"`
	DepartmentID *string `json:"department_id"`
}

type voucherIn struct {
	LegalEntityID string   `json:"legal_entity_id"`
	BusinessDate  string   `json:"business_date"`
	Description   string   `json:"description"`
	Version       *int64   `json:"version"`
	Lines         []lineIn `json:"lines"`
}

// prepared is a draft's input checked, with the legal entity's base currency and scale (outside the transaction).
type prepared struct {
	le    masterdata.LegalEntity
	lines []ledger.Line
}

func prepare(m *core.Base, ctx context.Context, in *voucherIn) (prepared, error) {
	var p prepared
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return p, core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case !core.DateForm.MatchString(in.BusinessDate):
		return p, core.Invalid("business_date", "business_date is YYYY-MM-DD")
	case strings.TrimSpace(in.Description) == "":
		return p, core.Invalid("description", "description is required")
	case len(in.Lines) < 2 || len(in.Lines) > core.MaxLines:
		return p, core.Invalid("lines", fmt.Sprintf("a voucher has 2-%d lines", core.MaxLines))
	}
	var err error
	if p.le, err = masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return p, err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, p.le.BaseCurrency)
	if err != nil {
		return p, err
	}
	for i, l := range in.Lines {
		field := fmt.Sprintf("lines[%d]", i)
		if l.Side != "debit" && l.Side != "credit" {
			return p, core.Invalid(field+".side", "side is debit or credit")
		}
		for name, v := range map[string]*string{"partner_id": l.PartnerID, "department_id": l.DepartmentID} {
			if v != nil && !core.UUIDForm.MatchString(*v) {
				return p, core.Invalid(field+"."+name, name+" is an ID")
			}
		}
		amount, err := core.Decimal(field+".amount", l.Amount, scale, true)
		if err != nil {
			return p, err
		}
		p.lines = append(p.lines, ledger.Line{AccountCode: l.AccountCode, Side: l.Side, Currency: p.le.BaseCurrency,
			Amount: amount, BaseAmount: amount, PartnerID: l.PartnerID, DepartmentID: l.DepartmentID})
	}
	return p, nil
}

// CreateVoucher makes a manual draft (VC01); its maker is the one who makes it.
func CreateVoucher(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in voucherIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Version != nil {
		return core.Invalid("version", "a new voucher has no version")
	}
	p, err := prepare(m, c.Ctx(), &in)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpVoucherEdit, core.EntityFacts(in.LegalEntityID))
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: in.LegalEntityID, BusinessDate: in.BusinessDate,
			PostingDate: in.BusinessDate, Description: in.Description, Source: ledger.Source{Kind: "manual"}, State: Draft,
			MakerID: &actor, Lines: p.lines})
		if err != nil {
			return idem.Outcome{}, err
		}
		v, err := ledger.Get(ctx, tx, id, false)
		if err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(id), core.Change(m, ctx, tx, actor, core.OpVoucherEdit, core.Vouchers, id, v)
	})
	if err != nil {
		return err
	}
	v, err := ledger.Read(m, ctx, core.ResultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, v)
}

func GetVoucher(m *core.Base, c *httpx.Context) error {
	v, err := ledger.Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpVoucherRead, core.EntityFacts(v.LegalEntityID)); err != nil {
		return err
	}
	return c.JSON(200, v)
}

// UpdateVoucher changes a draft (VC01): its date, description and whole lines, by version; the legal entity stays.
func UpdateVoucher(m *core.Base, c *httpx.Context) error {
	var in voucherIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	cur, err := ledger.Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpVoucherEdit, core.EntityFacts(cur.LegalEntityID))
	if err != nil {
		return err
	}
	if in.LegalEntityID != cur.LegalEntityID {
		return core.Invalid("legal_entity_id", "a voucher's legal entity does not change")
	}
	if d := Lifecycle.Decide(cur.State, Edit); d.Effect == core.Refuse {
		return core.Refused(d, cur.State)
	}
	p, err := prepare(m, ctx, &in)
	if err != nil {
		return err
	}
	var out ledger.Voucher
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := ledger.Get(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		if d := Lifecycle.Decide(v.State, Edit); d.Effect == core.Refuse {
			return core.Refused(d, v.State)
		}
		if in.Version != nil && *in.Version != v.Version {
			return core.ErrVersion.New(fmt.Sprintf("the voucher is at version %d", v.Version))
		}
		if err := ledger.Rewrite(ctx, tx, v.ID, ledger.Draft{LegalEntityID: v.LegalEntityID, BusinessDate: in.BusinessDate,
			Description: in.Description, Lines: p.lines}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE voucher SET version = version + 1, updated_at = now() WHERE id = $1`, v.ID); err != nil {
			return err
		}
		if out, err = ledger.Get(ctx, tx, v.ID, false); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpVoucherEdit, core.Vouchers, v.ID, out)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

type actionIn struct {
	Version *int64  `json:"version"`
	Reason  *string `json:"reason"`
}

// Action is one of submit, approve, reject and reverse, as the table decides it in the voucher's state; an ignored
// command answers the voucher as it is.
func Action(m *core.Base, command string) httpx.Handler {
	op := core.OpVoucherApprove
	if command == Submit {
		op = core.OpVoucherEdit
	}
	return func(c *httpx.Context) error {
		var in actionIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		cur, err := ledger.Read(m, c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, op, core.EntityFacts(cur.LegalEntityID))
		if err != nil {
			return err
		}
		d := Lifecycle.Decide(cur.State, command)
		if (command == Reject || command == Reverse) && d.Effect == core.Move &&
			(in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "a rejection and a reversal say why")
		}
		le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
		if err != nil {
			return err
		}
		today, err := bizdate.Today(le.Timezone)
		if err != nil {
			return err
		}
		var out ledger.Voucher
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			v, err := ledger.Get(ctx, tx, cur.ID, true)
			if err != nil {
				return err
			}
			out = v
			d := Lifecycle.Decide(v.State, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, v.State)
			}
			if in.Version != nil && *in.Version != v.Version {
				return core.ErrVersion.New(fmt.Sprintf("the voucher is at version %d", v.Version))
			}
			switch command {
			case Approve: // VC02, PD01, VC03
				if v.MakerID != nil && *v.MakerID == actor {
					return core.ErrApprover.New("the voucher was made by " + actor + ": another approves it")
				}
				if err := periods.CheckManual(m, ctx, tx, v.LegalEntityID, v.Period); err != nil {
					return err
				}
				out, err = ledger.Issue(m, ctx, tx, v.ID, le.Code, actor, d.Op, &actor)
				return err
			case Reverse: // VC04: a reversing voucher today, the sides swapped
				if err := periods.CheckManual(m, ctx, tx, v.LegalEntityID, today[:7]); err != nil {
					return err
				}
				lines := make([]ledger.Line, len(v.Lines))
				for i, l := range v.Lines {
					lines[i] = l
					lines[i].Side = map[string]string{"debit": "credit", "credit": "debit"}[l.Side]
				}
				id, err := ledger.Write(ctx, tx, ledger.Draft{LegalEntityID: v.LegalEntityID, BusinessDate: today,
					PostingDate: today, Description: "冲销 " + derefOr(v.Number, v.ID) + "：" + *in.Reason,
					Source: ledger.Source{Kind: "manual"}, State: Posted, MakerID: &actor, ReversesVoucherID: &v.ID, Lines: lines})
				if err != nil {
					return err
				}
				reversal, err := ledger.Issue(m, ctx, tx, id, le.Code, actor, d.Op, &actor)
				if err != nil {
					return err
				}
				if _, err := Transition(m, ctx, tx, v, d, in.Reason, &id, actor); err != nil {
					return err
				}
				out = reversal
				return nil
			}
			out, err = Transition(m, ctx, tx, v, d, in.Reason, nil, actor)
			return err
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

func derefOr(s *string, alt string) string {
	if s == nil {
		return alt
	}
	return *s
}

var listSpec = list.Spec{Operation: core.OpVoucherRead, Alias: "v", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "period", Column: "period", Kind: list.Text, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number", Column: "search", Fields: []string{"number"}}}}

// ListVouchers is one page, newest first, by legal entity, period, state and number.
func ListVouchers(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, p := range []string{"legal_entity_id", "period", "state"} {
		v := c.Query(p)
		if v == "" {
			continue
		}
		if p == "legal_entity_id" && !core.UUIDForm.MatchString(v) {
			return core.Invalid(p, p+" is an ID")
		}
		f = append(f, list.Filter{Field: p, Op: "eq", Values: []string{v}})
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, listSpec, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "voucher", ledger.Cols, ledger.Scan,
		func(v ledger.Voucher) ([]string, string) { return []string{v.CreatedAt}, v.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"vouchers": out, "next_cursor": next})
}
