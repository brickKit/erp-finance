-- 种最小可用的会计科目表 + FY2026 全年 12 个会计期间。
--
-- 科目：阶段二只预置 5 个（应收/应付/存货/主营业务收入/主营业务成本），
-- 够跑通"消费事件 → 生成凭证"这条链路即可——完整科目表是本地化的事，
-- 属于 customer_fork 或未来的 slot:coa（设计计划 §9 第 2 条）。
INSERT INTO accounts (code, name, category) VALUES
    ('1122', '应收账款',     'ASSET'),
    ('2202', '应付账款',     'LIABILITY'),
    ('1405', '库存商品',     'ASSET'),
    ('6001', '主营业务收入', 'REVENUE'),
    ('6401', '主营业务成本', 'EXPENSE');

-- 会计期间：一次性建好 FY2026 全年，不配后台自动建分区任务（设计计划
-- §9 第 6 条：开新会计年度是一次业务动作，不是日历滚动窗口）。
-- legal_entity_id 用 'default'——阶段二只有一个默认法人。
INSERT INTO fiscal_years (name, start_date, end_date) VALUES
    ('FY2026', '2026-01-01', '2026-12-31');

INSERT INTO accounting_periods (fiscal_year_id, period, legal_entity_id, start_date, end_date)
SELECT fy.id, p.period, 'default', p.start_date, p.end_date
FROM fiscal_years fy,
     (VALUES
        ('2026-01', DATE '2026-01-01', DATE '2026-01-31'),
        ('2026-02', DATE '2026-02-01', DATE '2026-02-28'),
        ('2026-03', DATE '2026-03-01', DATE '2026-03-31'),
        ('2026-04', DATE '2026-04-01', DATE '2026-04-30'),
        ('2026-05', DATE '2026-05-01', DATE '2026-05-31'),
        ('2026-06', DATE '2026-06-01', DATE '2026-06-30'),
        ('2026-07', DATE '2026-07-01', DATE '2026-07-31'),
        ('2026-08', DATE '2026-08-01', DATE '2026-08-31'),
        ('2026-09', DATE '2026-09-01', DATE '2026-09-30'),
        ('2026-10', DATE '2026-10-01', DATE '2026-10-31'),
        ('2026-11', DATE '2026-11-01', DATE '2026-11-30'),
        ('2026-12', DATE '2026-12-01', DATE '2026-12-31')
     ) AS p(period, start_date, end_date)
WHERE fy.name = 'FY2026';
