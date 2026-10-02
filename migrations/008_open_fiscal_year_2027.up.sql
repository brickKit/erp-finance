-- 开 2027 会计年度：与 003 建 FY2026 的方式相同——一个会计年度、12 个月度期间、
-- 分录行的 12 个分区。期间给每一个已有 FY2026 期间的法人都建一份（同一批法人、同样
-- 的粒度）。开账是一次业务动作，不配后台自动建；这份迁移必须在 2027-01-01 之前上线，
-- 否则从那天起每一次过账都找不到期间而失败（事件消费是至多一次，失败的消息不会再来）。
-- 可以重跑：已有的年度、期间、分区都跳过。
INSERT INTO fiscal_years (name, start_date, end_date) VALUES
    ('FY2027', '2027-01-01', '2027-12-31')
ON CONFLICT (name) DO NOTHING;

INSERT INTO accounting_periods (fiscal_year_id, period, legal_entity_id, start_date, end_date)
SELECT fy.id, p.period, le.legal_entity_id, p.start_date, p.end_date
FROM fiscal_years fy,
     (SELECT DISTINCT ap.legal_entity_id
        FROM accounting_periods ap JOIN fiscal_years f ON f.id = ap.fiscal_year_id
       WHERE f.name = 'FY2026') AS le,
     (VALUES
        ('2027-01', DATE '2027-01-01', DATE '2027-01-31'),
        ('2027-02', DATE '2027-02-01', DATE '2027-02-28'),
        ('2027-03', DATE '2027-03-01', DATE '2027-03-31'),
        ('2027-04', DATE '2027-04-01', DATE '2027-04-30'),
        ('2027-05', DATE '2027-05-01', DATE '2027-05-31'),
        ('2027-06', DATE '2027-06-01', DATE '2027-06-30'),
        ('2027-07', DATE '2027-07-01', DATE '2027-07-31'),
        ('2027-08', DATE '2027-08-01', DATE '2027-08-31'),
        ('2027-09', DATE '2027-09-01', DATE '2027-09-30'),
        ('2027-10', DATE '2027-10-01', DATE '2027-10-31'),
        ('2027-11', DATE '2027-11-01', DATE '2027-11-30'),
        ('2027-12', DATE '2027-12-01', DATE '2027-12-31')
     ) AS p(period, start_date, end_date)
WHERE fy.name = 'FY2027'
ON CONFLICT (period, legal_entity_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_01 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-01');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_02 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-02');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_03 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-03');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_04 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-04');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_05 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-05');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_06 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-06');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_07 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-07');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_08 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-08');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_09 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-09');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_10 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-10');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_11 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-11');
CREATE TABLE IF NOT EXISTS finance_journal_entry_lines_2027_12 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2027-12');
