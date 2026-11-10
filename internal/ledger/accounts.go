package ledger

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"
	"github.com/jackc/pgx/v5"
)

// Account is an account of a legal entity's chart.
type Account struct {
	ID            string  `json:"id"`
	LegalEntityID string  `json:"legal_entity_id"`
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Category      string  `json:"category"`
	ParentID      *string `json:"parent_id"`
	Postable      bool    `json:"postable"`
	State         string  `json:"state"`
	Version       int64   `json:"version"`
	UpdatedAt     string  `json:"updated_at"`
}

var codeForm = regexp.MustCompile(`^[0-9A-Z.-]{1,20}$`)

var categories = map[string]bool{"asset": true, "liability": true, "equity": true, "income": true, "expense": true}

const accountCols = `a.id::text, a.legal_entity_id::text, a.code, a.name, a.category, a.parent_id::text, a.postable, a.state,
	a.version, a.updated_at`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	var at time.Time
	err := row.Scan(&a.ID, &a.LegalEntityID, &a.Code, &a.Name, &a.Category, &a.ParentID, &a.Postable, &a.State, &a.Version, &at)
	a.UpdatedAt = canon.Time(at)
	return a, err
}

func getAccount(ctx context.Context, tx db.Tx, id string) (Account, error) {
	a, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountCols+` FROM account a WHERE a.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, core.NotFound("account")
	}
	return a, err
}

// Postable checks that an account of the legal entity may be posted to (VC01): it exists, is a leaf, is active.
func Postable(ctx context.Context, tx db.Tx, legalEntity, code string) error {
	var postable bool
	var state string
	err := tx.QueryRow(ctx, `SELECT postable, state FROM account WHERE legal_entity_id = $1 AND code = $2`,
		legalEntity, code).Scan(&postable, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrAccount.New("no account " + code)
	}
	if err != nil {
		return err
	}
	if !postable || state != "active" {
		return core.ErrAccount.New(fmt.Sprintf("account %s is not postable (a heading, or %s)", code, state))
	}
	return nil
}

type accountIn struct {
	LegalEntityID string  `json:"legal_entity_id"`
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Category      string  `json:"category"`
	ParentID      *string `json:"parent_id"`
	Postable      *bool   `json:"postable"`
}

// CreateAccount adds an account to a legal entity's chart; one below a heading only (a heading is not postable).
func CreateAccount(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in accountIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case !codeForm.MatchString(in.Code):
		return core.Invalid("code", "code is 1-20 of 0-9, A-Z, '.' and '-'")
	case strings.TrimSpace(in.Name) == "":
		return core.Invalid("name", "name is required")
	case !categories[in.Category]:
		return core.Invalid("category", "category is asset, liability, equity, income or expense")
	case in.ParentID != nil && !core.UUIDForm.MatchString(*in.ParentID):
		return core.Invalid("parent_id", "parent_id is an account's ID")
	}
	postable := in.Postable == nil || *in.Postable
	ctx, actor, err := core.Check(c, core.OpAccountManage, core.EntityFacts(in.LegalEntityID))
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		if in.ParentID != nil {
			p, err := getAccount(ctx, tx, *in.ParentID)
			if err != nil || p.LegalEntityID != in.LegalEntityID || p.Postable {
				return idem.Outcome{}, core.Invalid("parent_id", "the parent is a heading (not postable) of the same legal entity")
			}
		}
		id := ids.New().String()
		tag, err := tx.Exec(ctx, `INSERT INTO account (id, legal_entity_id, code, name, category, parent_id, postable,
			search, search_version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (legal_entity_id, code) DO NOTHING`,
			id, in.LegalEntityID, in.Code, in.Name, in.Category, in.ParentID, postable, search.Text(in.Code, in.Name), search.Version)
		if err != nil {
			return idem.Outcome{}, err
		}
		if tag.RowsAffected() == 0 {
			return idem.Outcome{}, core.ErrCode.New("account code " + in.Code + " is taken")
		}
		a, err := getAccount(ctx, tx, id)
		if err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(id), core.Change(m, ctx, tx, actor, core.OpAccountManage, core.Accounts, id, a)
	})
	if err != nil {
		return err
	}
	return readAccount(m, c, ctx, core.ResultID(out))
}

func readAccount(m *core.Base, c *httpx.Context, ctx context.Context, id string) error {
	var a Account
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = getAccount(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, a)
}

func GetAccount(m *core.Base, c *httpx.Context) error {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("account")
	}
	var a Account
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = getAccount(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpAccountRead, core.EntityFacts(a.LegalEntityID)); err != nil {
		return err
	}
	return c.JSON(200, a)
}

type accountPatch struct {
	Name    *string `json:"name"`
	State   *string `json:"state"`
	Version *int64  `json:"version"`
}

// UpdateAccount changes an account's name or state, by version.
func UpdateAccount(m *core.Base, c *httpx.Context) error {
	var in accountPatch
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return core.Invalid("name", "name is required")
	}
	if in.State != nil && *in.State != "active" && *in.State != "inactive" {
		return core.Invalid("state", "state is active or inactive")
	}
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("account")
	}
	var a Account
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = getAccount(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpAccountManage, core.EntityFacts(a.LegalEntityID))
	if err != nil {
		return err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM account WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		cur, err := getAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.Version != nil && *in.Version != cur.Version {
			return core.ErrVersion.New(fmt.Sprintf("the account is at version %d", cur.Version))
		}
		name, state := cur.Name, cur.State
		if in.Name != nil {
			name = *in.Name
		}
		if in.State != nil {
			state = *in.State
		}
		if _, err := tx.Exec(ctx, `UPDATE account SET name = $2, state = $3, search = $4, search_version = $5,
			version = version + 1, updated_at = now() WHERE id = $1`, id, name, state, search.Text(cur.Code, name),
			search.Version); err != nil {
			return err
		}
		if a, err = getAccount(ctx, tx, id); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpAccountManage, core.Accounts, id, a)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, a)
}

var accountList = list.Spec{Operation: core.OpAccountRead, Alias: "a", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "code", Column: "code", Kind: list.Text, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "category", Column: "category", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "code_name", Column: "search", Fields: []string{"code", "name"}}}}

// ListAccounts is one page of a legal entity's chart, by code.
func ListAccounts(m *core.Base, c *httpx.Context) error {
	le := c.Query("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	}
	f := []list.Filter{{Field: "legal_entity_id", Op: "eq", Values: []string{le}}}
	if cat := c.Query("category"); cat != "" {
		f = append(f, list.Filter{Field: "category", Op: "eq", Values: []string{cat}})
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, accountList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "code"}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "account", accountCols, scanAccount,
		func(a Account) ([]string, string) { return []string{a.Code}, a.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"accounts": out, "next_cursor": next})
}
