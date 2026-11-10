package periods

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/jackc/pgx/v5"
)

// Period is a period's full state (erp.accounting_period.changed.v1).
type Period struct {
	LegalEntityID string  `json:"legal_entity_id"`
	Period        string  `json:"period"`
	State         string  `json:"state"`
	Revision      int     `json:"revision"`
	Reason        *string `json:"reason"`
	Version       int64   `json:"version"`
	UpdatedAt     string  `json:"updated_at"`
}

const cols = `legal_entity_id::text, period, state, revision, reason, version, updated_at`

func scan(row pgx.Row) (Period, error) {
	var p Period
	var at time.Time
	err := row.Scan(&p.LegalEntityID, &p.Period, &p.State, &p.Revision, &p.Reason, &p.Version, &at)
	p.UpdatedAt = canon.Time(at)
	return p, err
}

// Of reads a month's period in tx, making it open the first time (PD01); lock takes its row's lock.
func Of(m *core.Base, ctx context.Context, tx db.Tx, legalEntity, month string, lock bool) (Period, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO period (legal_entity_id, period) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		legalEntity, month)
	if err != nil {
		return Period{}, err
	}
	q := `SELECT ` + cols + ` FROM period WHERE legal_entity_id = $1 AND period = $2`
	if lock {
		q += ` FOR UPDATE`
	}
	p, err := scan(tx.QueryRow(ctx, q, legalEntity, month))
	if err != nil || tag.RowsAffected() == 0 {
		return p, err
	}
	return p, emit(m, ctx, tx, p)
}

func emit(m *core.Base, ctx context.Context, tx db.Tx, p Period) error {
	_, err := m.RT.Events.Emit(ctx, tx, core.Stream, eventOf(p))
	return err
}

// Month is a date's month (YYYY-MM).
func Month(date string) string { return date[:7] }

func next(month string) string {
	t, _ := time.Parse("2006-01", month)
	return t.AddDate(0, 1, 0).Format("2006-01")
}

// PostingDate is a source's fact's posting date (PD01, protocol 06's late documents): the business date when its
// month takes postings, otherwise the first day of the next month that does (closed and reopened months take no
// fact: a reopening is for authorized corrections only). The business date never changes.
func PostingDate(m *core.Base, ctx context.Context, tx db.Tx, legalEntity, businessDate string) (string, error) {
	date, month := businessDate, Month(businessDate)
	for {
		p, err := Of(m, ctx, tx, legalEntity, month, false)
		if err != nil {
			return "", err
		}
		if p.State == Open {
			return date, nil
		}
		month = next(month)
		date = month + "-01"
	}
}

// CheckManual decides whether a manual voucher posts in a month (PD01): open or reopened; closed is PERIOD_CLOSED.
func CheckManual(m *core.Base, ctx context.Context, tx db.Tx, legalEntity, month string) error {
	p, err := Of(m, ctx, tx, legalEntity, month, true)
	if err != nil {
		return err
	}
	if d := Lifecycle.Decide(p.State, PostManual); d.Effect == core.Refuse {
		return core.ErrPeriodClosed.New(strings.ReplaceAll(d.Refusal, "{state}", p.State))
	}
	return nil
}

func ListPeriods(m *core.Base, c *httpx.Context) error {
	le := c.Query("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	}
	ctx, _, err := core.Check(c, core.OpPeriodRead, core.EntityFacts(le))
	if err != nil {
		return err
	}
	out := []Period{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		out = out[:0]
		q, args := `SELECT `+cols+` FROM period WHERE legal_entity_id = $1`, []any{le}
		if y := c.Query("year"); y != "" {
			q, args = q+` AND period LIKE $2`, append(args, y+"-%")
		}
		rows, err := tx.Query(ctx, q+` ORDER BY period`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"periods": out})
}

type reasonIn struct {
	Reason  *string `json:"reason"`
	Version *int64  `json:"version"`
}

// Action closes or reopens a period, as the table decides it.
func Action(m *core.Base, command string) httpx.Handler {
	return func(c *httpx.Context) error {
		le, month := c.Param("legal_entity_id"), c.Param("period")
		if !core.UUIDForm.MatchString(le) || len(month) != 7 {
			return core.NotFound("period")
		}
		var in reasonIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		if command == Reopen && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "a reopening says why")
		}
		ctx, actor, err := core.Check(c, core.OpPeriodClose, core.EntityFacts(le))
		if err != nil {
			return err
		}
		var out Period
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			p, err := Of(m, ctx, tx, le, month, true)
			if err != nil {
				return err
			}
			out = p
			d := Lifecycle.Decide(p.State, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, p.State)
			}
			if d.Guard == GuardNothingWaiting {
				if err := nothingWaiting(ctx, tx, le, month); err != nil {
					return err
				}
			}
			revision := p.Revision
			if command == Reopen {
				revision++
			}
			if _, err := tx.Exec(ctx, `UPDATE period SET state = $3, revision = $4, reason = $5, version = version + 1,
				updated_at = now() WHERE legal_entity_id = $1 AND period = $2`, le, month, d.To, revision, in.Reason); err != nil {
				return err
			}
			if out, err = scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM period WHERE legal_entity_id = $1 AND period = $2`,
				le, month)); err != nil {
				return err
			}
			if err := emit(m, ctx, tx, out); err != nil {
				return err
			}
			return core.Change(m, ctx, tx, actor, d.Op, core.Periods, le+"/"+month, out)
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// nothingWaiting is PD02's guard: no manual voucher of the month waits for approval, and every month from the legal
// entity's first period to this one is closed or reopened (a month never posted to is open).
func nothingWaiting(ctx context.Context, tx db.Tx, le, month string) error {
	var waiting int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM voucher WHERE legal_entity_id = $1 AND period = $2
		AND state = 'submitted'`, le, month).Scan(&waiting); err != nil {
		return err
	}
	if waiting > 0 {
		return core.ErrState.New(fmt.Sprintf("%d manual vouchers of %s wait for approval", waiting, month))
	}
	var first string
	err := tx.QueryRow(ctx, `SELECT min(period) FROM period WHERE legal_entity_id = $1`, le).Scan(&first)
	if errors.Is(err, pgx.ErrNoRows) || first == "" {
		return err
	}
	for m := first; m < month; m = next(m) {
		var state string
		err := tx.QueryRow(ctx, `SELECT state FROM period WHERE legal_entity_id = $1 AND period = $2`, le, m).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) || state == Open {
			return core.ErrState.New(m + " is open: the earlier months close first")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
