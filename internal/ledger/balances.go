package ledger

import (
	"context"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

// Balance is an account's month in the base currency: opening, debits, credits, closing (debit positive).
type Balance struct {
	AccountCode string `json:"account_code"`
	Opening     string `json:"opening"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	Closing     string `json:"closing"`
}

// ListBalances answers each account with postings up to the month: posted and reversed vouchers count (a reversal is
// its own voucher).
func ListBalances(m *core.Base, c *httpx.Context) error {
	le, month := c.Query("legal_entity_id"), c.Query("period")
	if !core.UUIDForm.MatchString(le) || len(month) != 7 {
		return core.Invalid("period", "legal_entity_id and period (YYYY-MM) are required")
	}
	ctx, _, err := core.Check(c, core.OpVoucherRead, core.EntityFacts(le))
	if err != nil {
		return err
	}
	out := []Balance{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		out = out[:0]
		rows, err := tx.Query(ctx, `SELECT l.account_code,
			trim_scale(coalesce(sum(CASE WHEN v.period < $2 THEN CASE l.side WHEN 'debit' THEN l.base_amount ELSE -l.base_amount END END), 0))::text,
			trim_scale(coalesce(sum(CASE WHEN v.period = $2 AND l.side = 'debit' THEN l.base_amount END), 0))::text,
			trim_scale(coalesce(sum(CASE WHEN v.period = $2 AND l.side = 'credit' THEN l.base_amount END), 0))::text,
			trim_scale(coalesce(sum(CASE l.side WHEN 'debit' THEN l.base_amount ELSE -l.base_amount END), 0))::text
			FROM voucher_line l JOIN voucher v ON v.id = l.voucher_id
			WHERE v.legal_entity_id = $1 AND v.state IN ('posted', 'reversed') AND v.period <= $2
			GROUP BY l.account_code ORDER BY l.account_code`, le, month)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Balance
			if err := rows.Scan(&b.AccountCode, &b.Opening, &b.Debit, &b.Credit, &b.Closing); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"balances": out})
}
