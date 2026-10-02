-- 应收台账显示客户名、按到期日算账龄。
--
-- 客户名放在客户摘要副本里（消费 mdm.customer.created/updated.v1 时一并写入），
-- 读应收时按 customer_id 关联：改名之后列表显示的是新名字。还没收到过这个客户的
-- 事件时为空串。
ALTER TABLE customer_credit_snapshots ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';

-- 到期日：写应收时定下，账龄 = 统计当天 − 到期日。上游事件（sales.order.created.v1）
-- 不带付款条件，所以到期日就是记账当天（见即付）；已有的行按创建日期补齐。
ALTER TABLE ar_ledger ADD COLUMN IF NOT EXISTS due_date DATE;
UPDATE ar_ledger SET due_date = (created_at AT TIME ZONE 'UTC')::date WHERE due_date IS NULL;
ALTER TABLE ar_ledger ALTER COLUMN due_date SET NOT NULL;
CREATE INDEX IF NOT EXISTS ar_ledger_entity_due ON ar_ledger (legal_entity_id, due_date);
