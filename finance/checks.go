package finance

import (
	"context"

	core "github.com/brickKit/erp-finance/v3/internal/core"
	intake "github.com/brickKit/erp-finance/v3/internal/intake"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/recovery"
)

// RecoveryChecks registers what a full recovery asks this member (be-protocol chapter 14; docs/design.md, "整套恢复
// 之后"): whether the objects its audit names are still there and at which version, how it voids the voucher numbers
// a raise skips over (P14.5), and its own check (RC01).
func (m *Module) RecoveryChecks(c *recovery.Checks) error {
	accounts := recovery.TablePresence("account", "id", "version")
	mappings := query(`SELECT 'mapping/' || legal_entity_id::text, version FROM mapping_version
		WHERE 'mapping/' || legal_entity_id::text = ANY($1)`)
	c.Presence(core.Accounts, func(ctx context.Context, tx db.Tx, ids []string) (map[string]int64, error) {
		out, err := accounts(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		more, err := mappings(ctx, tx, ids)
		for k, v := range more {
			out[k] = v
		}
		return out, err
	})
	c.Presence(core.Vouchers, recovery.TablePresence("voucher", "id", "version"))
	c.Presence(core.Periods, query(`SELECT legal_entity_id::text || '/' || period, version FROM period
		WHERE legal_entity_id::text || '/' || period = ANY($1)`))
	c.Presence(core.OpenItems, recovery.TablePresence("open_item", "id", "version"))
	c.Presence(core.SupplierInvoices, recovery.TablePresence("supplier_invoice", "id", "version"))
	c.Presence(core.System, recovery.TablePresence("pay_claim", "claim_id", "version")) // the payment claims
	c.Void(voidNumbers)
	return c.Add(recovery.Check{Name: intake.CheckPendingFacts, Basis: "装配项目协议 05 的共同规则（不可遗漏的增量事实）；协议 15 第 8 步",
		Run: recovery.InTx(intake.PendingFacts)})
}

// voidNumbers registers the JV numbers a full recovery raised the series over (P14.5).
func voidNumbers(ctx context.Context, tx db.Tx, s db.Series, from, to int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO voided_number (legal_entity_id, series, period, number, reason)
		SELECT $1::uuid, $2, $3, n, 'a full recovery lost the voucher posted under this number'
		FROM generate_series($4::bigint, $5::bigint) n ON CONFLICT DO NOTHING`, s.LegalEntity, s.Code, s.Period, from, to)
	return err
}

// query is a presence answered by a query of (id, version) over the ids asked ($1).
func query(q string) recovery.PresenceFunc {
	return func(ctx context.Context, tx db.Tx, ids []string) (map[string]int64, error) {
		rows, err := tx.Query(ctx, q, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var k string
			var v int64
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, rows.Err()
	}
}
