package repo

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// UpsertCustomerSnapshotTx 维护 mdm-customer 的客户摘要副本：额度值（credit_limit，
// 超限判定用）与客户名（应收台账显示用）。供 backend/internal/consumer 在
// besdk.Consume 给的事务里调用，所以接 *sql.Tx 而不是自己开 besdk.WithTx。
//
// WHERE version < EXCLUDED.version 是按聚合版本单调更新：besdk.Consume 的
// event_inbox 只在同一个 subject 内保证单调，created.v1 和 updated.v1 是两个
// subject，跨 subject 的乱序要在这一层再挡一次。
//
// credit_limit 进 NUMERIC 之前先过 parseCents：上游的校验不归本组件管（已发布的
// mdm-customer 2.0.0 用 ParseFloat，"NaN" 能通过），NaN 存进来以后这个客户的每一张
// 销售订单都过不了账。不合法的值按"未配置额度"（0）存，名字照常更新，记一条 Warn。
// logger 为 nil 时不记。
func UpsertCustomerSnapshotTx(tx *sql.Tx, customerID, name, creditLimit string, version int64, logger *slog.Logger) error {
	if _, err := parseCents("credit_limit", creditLimit); err != nil {
		orDiscard(logger).Warn("mdm-customer 事件里的 credit_limit 不是合法金额，按未配置额度存",
			"customer_id", customerID, "credit_limit", creditLimit)
		creditLimit = "0"
	}
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
