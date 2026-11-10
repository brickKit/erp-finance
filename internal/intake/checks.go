package intake

import (
	"context"
	"fmt"
	"time"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/recovery"
)

// CheckPendingFacts is RC01: the facts received and not posted.
const CheckPendingFacts = "erp.finance.pending_facts"

// PendingFacts reports every received fact not posted yet (RC01), oldest first; read only.
func PendingFacts(ctx context.Context, tx db.Tx) (recovery.Result, error) {
	rows, err := tx.Query(ctx, `SELECT fact_id::text, type, source, received_at FROM fact_received WHERE state = 'pending'
		ORDER BY received_at, fact_id`)
	if err != nil {
		return recovery.Result{}, err
	}
	defer rows.Close()
	res := recovery.Result{Outcome: recovery.Pass}
	for rows.Next() {
		var d recovery.Difference
		var typ, source string
		var received time.Time
		if err := rows.Scan(&d.ObjectID, &typ, &source, &received); err != nil {
			return res, err
		}
		d.ObjectType = "fact_received"
		d.Detail = fmt.Sprintf("%s from %s, received %s, not posted", typ, source, canon.Time(received))
		res.Differences = append(res.Differences, d)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(res.Differences) > 0 {
		res.Outcome = recovery.Diff
		res.Disposal = "补映射或补规则后自动过账；等人处理的按日志事件 fact_not_postable、reversal_partial、" +
			"reversal_credit_used 核对来源后更正（docs/design.md RC01）"
	}
	return res, nil
}
