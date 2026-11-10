package ledger

import (
	"context"
	"fmt"
	"sort"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

// Entry maps one role of a rule to an account.
type Entry struct {
	RuleKey     string `json:"rule_key"`
	Role        string `json:"role"`
	AccountCode string `json:"account_code"`
}

// Mapping is a legal entity's mapping; version 0: never set.
type Mapping struct {
	LegalEntityID string  `json:"legal_entity_id"`
	Entries       []Entry `json:"entries"`
	Version       *int64  `json:"version"`
}

func mappingOf(ctx context.Context, tx db.Tx, le string) (Mapping, error) {
	var v int64
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT version FROM mapping_version WHERE legal_entity_id = $1), 0)`,
		le).Scan(&v); err != nil {
		return Mapping{}, err
	}
	out := Mapping{LegalEntityID: le, Entries: []Entry{}, Version: &v}
	rows, err := tx.Query(ctx, `SELECT rule_key, role, account_code FROM account_mapping WHERE legal_entity_id = $1
		ORDER BY rule_key, role`, le)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.RuleKey, &e.Role, &e.AccountCode); err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, e)
	}
	return out, rows.Err()
}

func GetMapping(m *core.Base, c *httpx.Context) error {
	le := c.Param("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.NotFound("mapping")
	}
	ctx, _, err := core.Check(c, core.OpAccountRead, core.EntityFacts(le))
	if err != nil {
		return err
	}
	var out Mapping
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		out, err = mappingOf(ctx, tx, le)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// PutMapping replaces a legal entity's mapping whole, by version: every entry a role of a rule in the catalog, every
// account postable.
func PutMapping(m *core.Base, c *httpx.Context) error {
	le := c.Param("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.NotFound("mapping")
	}
	var in Mapping
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, e := range in.Entries {
		field := fmt.Sprintf("entries[%d]", i)
		if _, ok := Rules[e.RuleKey][e.Role]; !ok {
			return core.Invalid(field, "no role "+e.Role+" in a rule "+e.RuleKey)
		}
		if seen[e.RuleKey+"/"+e.Role] {
			return core.Invalid(field, "a rule's role is mapped once")
		}
		seen[e.RuleKey+"/"+e.Role] = true
	}
	ctx, actor, err := core.Check(c, core.OpAccountManage, core.EntityFacts(le))
	if err != nil {
		return err
	}
	var out Mapping
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		// the version decides in the statement itself: two changes on the same version, one wins
		q, args := `INSERT INTO mapping_version (legal_entity_id, version) VALUES ($1, 1) ON CONFLICT (legal_entity_id)
			DO UPDATE SET version = mapping_version.version + 1, updated_at = now()`, []any{le}
		switch {
		case in.Version != nil && *in.Version == 0:
			q = `INSERT INTO mapping_version (legal_entity_id, version) VALUES ($1, 1) ON CONFLICT DO NOTHING`
		case in.Version != nil:
			q = `UPDATE mapping_version SET version = version + 1, updated_at = now() WHERE legal_entity_id = $1 AND version = $2`
			args = append(args, *in.Version)
		}
		tag, err := tx.Exec(ctx, q, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			cur, err := mappingOf(ctx, tx, le)
			if err != nil {
				return err
			}
			return core.ErrVersion.New(fmt.Sprintf("the mapping is at version %d", *cur.Version))
		}
		for _, e := range in.Entries {
			if err := Postable(ctx, tx, le, e.AccountCode); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM account_mapping WHERE legal_entity_id = $1`, le); err != nil {
			return err
		}
		for _, e := range in.Entries {
			if _, err := tx.Exec(ctx, `INSERT INTO account_mapping (legal_entity_id, rule_key, role, account_code)
				VALUES ($1, $2, $3, $4)`, le, e.RuleKey, e.Role, e.AccountCode); err != nil {
				return err
			}
		}
		if out, err = mappingOf(ctx, tx, le); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpAccountManage, core.Accounts, "mapping/"+le, out)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// Resolve turns a rule's role amounts into voucher lines on the legal entity's accounts (protocol 05: the source
// names roles, never accounts); a role the rule has not, or the legal entity has not mapped, is refused.
func Resolve(ctx context.Context, tx db.Tx, le, rule string, amounts map[string]string) ([]Line, error) {
	sides, ok := Rules[rule]
	if !ok {
		return nil, core.ErrMapping.New("no posting rule " + rule)
	}
	roles := make([]string, 0, len(amounts))
	for r := range amounts {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool { // debits first, then by role
		if sides[roles[i]] != sides[roles[j]] {
			return sides[roles[i]] == "debit"
		}
		return roles[i] < roles[j]
	})
	var out []Line
	for _, r := range roles {
		side, ok := sides[r]
		if !ok {
			return nil, core.ErrMapping.New("rule " + rule + " has no role " + r)
		}
		var code string
		if err := tx.QueryRow(ctx, `SELECT account_code FROM account_mapping WHERE legal_entity_id = $1 AND rule_key = $2
			AND role = $3`, le, rule, r).Scan(&code); err != nil {
			return nil, core.ErrMapping.New("the legal entity maps no account to " + rule + "/" + r)
		}
		out = append(out, Line{AccountCode: code, Side: side, Amount: amounts[r]})
	}
	return out, nil
}

// PostingRule is a rule as the user surface lists it.
type PostingRule struct {
	RuleKey string            `json:"rule_key"`
	Roles   map[string]string `json:"roles"`
}

func ListPostingRules(m *core.Base, c *httpx.Context) error {
	out := []PostingRule{}
	for _, k := range RuleKeys() {
		out = append(out, PostingRule{RuleKey: k, Roles: Rules[k]})
	}
	return c.JSON(200, map[string]any{"rules": out})
}
