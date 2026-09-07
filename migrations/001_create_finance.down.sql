DROP TABLE IF EXISTS customer_credit_snapshots;
DROP TABLE IF EXISTS customer_credit_exposure;
DROP TABLE IF EXISTS ap_ledger;
DROP TABLE IF EXISTS ar_ledger;
DROP TABLE IF EXISTS finance_journal_entry_lines;   -- CASCADE 到所有期间分区
DROP TABLE IF EXISTS finance_journal_entries;
DROP SEQUENCE IF EXISTS entry_no_seq;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS accounting_periods;
DROP TABLE IF EXISTS fiscal_years;
