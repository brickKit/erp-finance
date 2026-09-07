DELETE FROM accounting_periods WHERE fiscal_year_id IN (SELECT id FROM fiscal_years WHERE name = 'FY2026');
DELETE FROM fiscal_years WHERE name = 'FY2026';
DELETE FROM accounts WHERE code IN ('1122', '2202', '1405', '6001', '6401');
