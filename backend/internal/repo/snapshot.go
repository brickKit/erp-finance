package repo

import (
	"database/sql"
	"fmt"
)

// UpsertCustomerSnapshotTx 维护 mdm-customer 的客户摘要副本：额度值（credit_limit，
// 超限判定用）与客户名（应收台账显示用）。供 backend/internal/consumer 在
// besdk.Consume 给的事务里调用，所以接 *sql.Tx 而不是自己开 besdk.WithTx。
//
// WHERE version < EXCLUDED.version 是按聚合版本单调更新：besdk.Consume 的
// event_inbox 只在同一个 subject 内保证单调，created.v1 和 updated.v1 是两个
// subject，跨 subject 的乱序要在这一层再挡一次。
func UpsertCustomerSnapshotTx(tx *sql.Tx, customerID, name, creditLimit string, version int64) error {
	if creditLimit == "" {
		creditLimit = "0"
	}
	_, err := tx.Exec(`
		INSERT INTO customer_credit_snapshots (customer_id, name, credit_limit, version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (customer_id) DO UPDATE
		   SET name = EXCLUDED.name, credit_limit = EXCLUDED.credit_limit,
		       version = EXCLUDED.version, updated_at = now()
		 WHERE customer_credit_snapshots.version < EXCLUDED.version`,
		customerID, name, creditLimit, version)
	if err != nil {
		return fmt.Errorf("写 customer_credit_snapshots: %w", err)
	}
	return nil
}
