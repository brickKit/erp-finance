DROP INDEX IF EXISTS ar_ledger_entity_due;
ALTER TABLE ar_ledger DROP COLUMN IF EXISTS due_date;
ALTER TABLE customer_credit_snapshots DROP COLUMN IF EXISTS name;
