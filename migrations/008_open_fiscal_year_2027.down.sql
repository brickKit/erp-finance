-- 撤掉 FY2027。2027 年已经有凭证时这条会失败（分录行与凭证头引用着这些期间），
-- 那时就不该再撤。
DROP TABLE IF EXISTS finance_journal_entry_lines_2027_01, finance_journal_entry_lines_2027_02,
  finance_journal_entry_lines_2027_03, finance_journal_entry_lines_2027_04,
  finance_journal_entry_lines_2027_05, finance_journal_entry_lines_2027_06,
  finance_journal_entry_lines_2027_07, finance_journal_entry_lines_2027_08,
  finance_journal_entry_lines_2027_09, finance_journal_entry_lines_2027_10,
  finance_journal_entry_lines_2027_11, finance_journal_entry_lines_2027_12;
DELETE FROM accounting_periods WHERE period LIKE '2027-%';
DELETE FROM fiscal_years WHERE name = 'FY2027';
