// 客户信用额度：本组件只持有"已用额度"（exposure），额度值本身在
// mdm-customer，走事件摘要副本（customer_credit_snapshots），设计计划
// §5 的三方分工。
package repo

import (
	"context"
	"database/sql"
	"fmt"

	besdk "github.com/brickKit/be-sdk-go"
)

type CreditExposure struct {
	CustomerID string
	Exposure   string
	Version    int64
}

func (r *Repo) GetCreditExposure(ctx context.Context, customerID string) (*CreditExposure, error) {
	if customerID == "" {
		return nil, fmt.Errorf("%w: customer_id 不能为空", ErrInvalidArgument)
	}
	var ce CreditExposure
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			`SELECT customer_id, exposure, version FROM customer_credit_exposure WHERE customer_id = $1`, customerID)
		err := row.Scan(&ce.CustomerID, &ce.Exposure, &ce.Version)
		if err == sql.ErrNoRows {
			ce = CreditExposure{CustomerID: customerID, Exposure: "0"}
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("查 customer_credit_exposure: %w", err)
	}
	return &ce, nil
}

// BatchGetCreditExposure 是防 N+1 的唯一合法调用方式（§3.8）。缺失的
// 客户视为已用额度 0（同 GetCreditExposure 的判据）。
func (r *Repo) BatchGetCreditExposure(ctx context.Context, customerIDs []string) ([]*CreditExposure, error) {
	if len(customerIDs) == 0 {
		return nil, nil
	}
	var out []*CreditExposure
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT customer_id, exposure, version FROM customer_credit_exposure
			 WHERE customer_id = ANY($1::text[])`, customerIDs)
		if err != nil {
			return fmt.Errorf("查 customer_credit_exposure: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ce CreditExposure
			if err := rows.Scan(&ce.CustomerID, &ce.Exposure, &ce.Version); err != nil {
				return err
			}
			out = append(out, &ce)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// applyCreditExposureDeltaTx 累加/冲减已用额度，返回过账后的新值。用
// 条件更新的思路做首次 upsert（同 erp-inventory Receive/Adjust 的两步
// 写法：先 INSERT ... ON CONFLICT DO NOTHING 保证行存在，再 UPDATE）。
func applyCreditExposureDeltaTx(ctx context.Context, tx *sql.Tx, customerID, delta string) (string, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO customer_credit_exposure (customer_id, exposure) VALUES ($1, 0)
		ON CONFLICT (customer_id) DO NOTHING`, customerID); err != nil {
		return "", fmt.Errorf("写 customer_credit_exposure: %w", err)
	}
	var newExposure string
	err := tx.QueryRowContext(ctx, `
		UPDATE customer_credit_exposure
		   SET exposure = exposure + $1, version = version + 1, updated_at = now()
		 WHERE customer_id = $2
		RETURNING exposure`, delta, customerID).Scan(&newExposure)
	if err != nil {
		return "", fmt.Errorf("更新 customer_credit_exposure: %w", err)
	}
	return newExposure, nil
}

// getCreditLimitTx 读客户的额度值——查不到摘要副本（还没消费到
// mdm.customer.* 事件，或客户从没设过额度）视为 0，不报错。
func getCreditLimitTx(ctx context.Context, tx *sql.Tx, customerID string) (string, error) {
	var limit string
	err := tx.QueryRowContext(ctx,
		`SELECT credit_limit FROM customer_credit_snapshots WHERE customer_id = $1`, customerID).Scan(&limit)
	if err == sql.ErrNoRows {
		return "0", nil
	}
	if err != nil {
		return "", fmt.Errorf("查 customer_credit_snapshots: %w", err)
	}
	return limit, nil
}
