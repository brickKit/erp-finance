package repo

import (
	"database/sql"
	"fmt"
)

// UpsertCustomerCreditSnapshotTx 维护 mdm-customer 的摘要副本——只取
// credit_limit（额度值本身，设计计划 §5 的三方分工）。供
// backend/internal/consumer 在 besdk.Consume 给的事务里调用，因此接
// *sql.Tx 而不是自己开 besdk.WithTx（同 erp-inventory 的判据）。
//
// ⚠️ WHERE version < $3 是按 version 单调更新（§3.10）：besdk.Consume
// 的 event_inbox 只在同一个 subject 内保证单调，created.v1 和
// updated.v1 是两个不同 subject，跨 subject 的乱序必须在这一层再挡一次
// （同 erp-inventory 的 UpsertProductTrackingSnapshotTx）。
func UpsertCustomerCreditSnapshotTx(tx *sql.Tx, customerID, creditLimit string, version int64) error {
	if creditLimit == "" {
		creditLimit = "0"
	}
	_, err := tx.Exec(`
		INSERT INTO customer_credit_snapshots (customer_id, credit_limit, version)
		VALUES ($1, $2, $3)
		ON CONFLICT (customer_id) DO UPDATE
		   SET credit_limit = EXCLUDED.credit_limit, version = EXCLUDED.version, updated_at = now()
		 WHERE customer_credit_snapshots.version < EXCLUDED.version`,
		customerID, creditLimit, version)
	if err != nil {
		return fmt.Errorf("写 customer_credit_snapshots: %w", err)
	}
	return nil
}
