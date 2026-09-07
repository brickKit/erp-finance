-- erp-finance 核心表：会计年度/期间、科目表、凭证头/明细、应收/应付台账、
-- 客户信用额度（已用值 + 摘要副本）。schema 由迁移工具的 search_path 指定，
-- SQL 里不写限定名。
-- ⚠️ 迁移状态表必须落在本组件 schema 里（§11.2.3）：golang-migrate 的
-- x-migrations-table + search_path，见 backend/cmd/migrate/main.go

-- 会计年度，几行量级，不分区。
CREATE TABLE fiscal_years (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT           NOT NULL,   -- 如 'FY2026'
    start_date DATE           NOT NULL,
    end_date   DATE           NOT NULL,
    -- §11.2.1 强制字段
    created_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version    BIGINT         NOT NULL DEFAULT 1,
    status     TEXT           NOT NULL DEFAULT 'ACTIVE'
);
CREATE UNIQUE INDEX fiscal_years_name_uniq ON fiscal_years (name);

-- 会计期间，三态（设计计划 §2.2）：OPEN -close-> CLOSED -lock-> LOCKED
-- （终态）；CLOSED -reopen-> OPEN。自然键是 (period, legal_entity_id)，
-- 不是数字 id——CheckPeriodOpen/ClosePeriod 等 rpc 都按这两个字段查。
--
-- ⚠️ 阶段二只有一个默认法人，过滤逻辑到阶段三才实现，但 legal_entity_id
-- 列必须现在就建——分区表回头加列的代价比建表时多两个数量级（§11.2.1）。
CREATE TABLE accounting_periods (
    id              BIGSERIAL PRIMARY KEY,
    fiscal_year_id  BIGINT         NOT NULL REFERENCES fiscal_years (id),
    period          TEXT           NOT NULL,   -- 'YYYY-MM'
    legal_entity_id TEXT           NOT NULL,
    start_date      DATE           NOT NULL,
    end_date        DATE           NOT NULL,
    status          TEXT           NOT NULL DEFAULT 'OPEN',
    -- post_no 的计数器，同一期间内连续无缺口（设计计划 §2.1）。⚠️ 这不是
    -- Frappe naming series那种"全局热行 + SELECT FOR UPDATE"（设计计划
    -- §8 明确列为反面教材）——这里的锁范围只是"这一个期间"，不同期间的
    -- 过账互不阻塞；而"过账时在同一事务里锁住期间行判状态"本来就是
    -- §3.1 要求的权威判定第二层，计数器搭这个已有的锁便车，不额外加锁。
    last_post_seq   BIGINT         NOT NULL DEFAULT 0,
    -- §11.2.1 强制字段（status 复用为期间状态，不是另加一列）
    created_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version         BIGINT         NOT NULL DEFAULT 1,
    CONSTRAINT accounting_periods_status_valid CHECK (status IN ('OPEN', 'CLOSED', 'LOCKED'))
);
CREATE UNIQUE INDEX accounting_periods_period_entity_uniq ON accounting_periods (period, legal_entity_id);
CREATE INDEX accounting_periods_fiscal_year ON accounting_periods (fiscal_year_id);

-- 会计科目表，几十行量级，不分区。
CREATE TABLE accounts (
    id         BIGSERIAL PRIMARY KEY,
    code       TEXT           NOT NULL,
    name       TEXT           NOT NULL,
    category   TEXT           NOT NULL,   -- ASSET/LIABILITY/EQUITY/REVENUE/EXPENSE
    -- §11.2.1 强制字段
    created_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version    BIGINT         NOT NULL DEFAULT 1,
    status     TEXT           NOT NULL DEFAULT 'ACTIVE',
    CONSTRAINT accounts_category_valid
      CHECK (category IN ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE'))
);
CREATE UNIQUE INDEX accounts_code_uniq ON accounts (code);

-- 凭证头。⚠️ 必须不分区（设计计划 §2.3）：幂等过账的唯一约束
-- （source_component/source_doc_type/source_doc_id/source_revision）
-- 建在分区表上，PostgreSQL 要求分区键必须是唯一约束的一部分——那等于
-- "同一张源单在不同期间可以过两次账"，正好是要防的事。所以真相源必须
-- 是这张不分区的头表，分录明细才按期间分区（见下）。
--
-- 两个单号分开（设计计划 §2.1）：entry_no 创建时分配，允许有缺口；
-- post_no 过账时才分配，同一期间内必须连续无缺口（审计要求）——
-- 阶段二先只保证唯一，连续性留给 Task 14 实现时用同期间内的序列生成。
--
-- entry_no 允许有缺口（设计计划 §2.1），用普通序列就够——不像 post_no
-- 那样需要"同一期间连续"，所以不需要按期间分开、也不需要搭期间行的锁。
CREATE SEQUENCE entry_no_seq;

CREATE TABLE finance_journal_entries (
    id               BIGSERIAL PRIMARY KEY,
    entry_no         TEXT           NOT NULL,
    post_no          TEXT           NOT NULL DEFAULT '',  -- 空串 = 还没过账
    period           TEXT           NOT NULL,
    legal_entity_id  TEXT           NOT NULL,
    status           TEXT           NOT NULL DEFAULT 'DRAFT',  -- DRAFT/POSTED，没有 CANCELLED（§2.1）
    -- 幂等过账用：自动凭证（消费事件生成）才有值，人工凭证
    -- （PostManualEntry）全部留空——它没有"源单"的概念。
    source_component TEXT           NOT NULL DEFAULT '',
    source_doc_type  TEXT           NOT NULL DEFAULT '',
    source_doc_id    TEXT           NOT NULL DEFAULT '',
    source_revision  BIGINT         NOT NULL DEFAULT 0,
    memo             TEXT           NOT NULL DEFAULT '',
    -- §11.2.1 强制字段
    created_at       TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version          BIGINT         NOT NULL DEFAULT 1,
    posted_at        TIMESTAMPTZ,
    CONSTRAINT finance_journal_entries_status_valid CHECK (status IN ('DRAFT', 'POSTED')),
    CONSTRAINT finance_journal_entries_period_fkey
      FOREIGN KEY (period, legal_entity_id) REFERENCES accounting_periods (period, legal_entity_id)
);
CREATE UNIQUE INDEX finance_journal_entries_entry_no_uniq ON finance_journal_entries (entry_no);
-- post_no 只在过账后有值；DRAFT 状态下全部是空串，不能对空串做唯一约束
-- （否则第二张草稿就冲突了），所以排除空串。
CREATE UNIQUE INDEX finance_journal_entries_post_no_uniq
  ON finance_journal_entries (post_no) WHERE post_no != '';
-- ⚠️ 这条是全组件最容易做错的一处（设计计划 §2.3 三个坑叠在一起）：
-- inbox 幂等防的是"同一条消息投两次"，这条唯一约束防的是"两条不同消息
-- 指向同一张源单"——两层都要有。WHERE 排除人工凭证（它们 source_component
-- 恒为空串，不该被这条约束互相拦住）。
CREATE UNIQUE INDEX finance_journal_entries_source_uniq
  ON finance_journal_entries (source_component, source_doc_type, source_doc_id, source_revision)
  WHERE source_component != '';

-- 分录明细。按 accounting_period 做 LIST 分区（不是 RANGE）——期间是
-- 离散的业务标识符（'2026-09' 这个值本身就是分区键，不是时间戳落在
-- 哪个区间），LIST 分区比 RANGE 更贴切。⚠️ 这是全系统唯一不按
-- created_at 分区的表（§11.2.5）。
--
-- accounting_period/legal_entity_id 都是从头表冗余过来的（避免每次
-- 数据权限过滤都要 JOIN 头表）——写入时必须与所属 entry_id 的头表数据
-- 一致，这是应用层的不变式，数据库管不了跨表一致性。
CREATE TABLE finance_journal_entry_lines (
    id                BIGSERIAL,
    entry_id          BIGINT         NOT NULL REFERENCES finance_journal_entries (id),
    accounting_period TEXT           NOT NULL,
    legal_entity_id   TEXT           NOT NULL,
    account_id        BIGINT         NOT NULL REFERENCES accounts (id),
    -- ⚠️ 标准版仅支持单币种（设计书 §5.5），但不写这一列是错的——留一个
    -- 恒为本位币的列，比将来加列便宜两个数量级（阶段计划 Task 14 ④）。
    -- 不写任何汇率逻辑，多币种走 customer_fork。
    currency          TEXT           NOT NULL DEFAULT 'CNY',
    debit             NUMERIC(18,2)  NOT NULL DEFAULT 0,
    credit            NUMERIC(18,2)  NOT NULL DEFAULT 0,
    memo              TEXT           NOT NULL DEFAULT '',
    -- §11.2.1 强制字段
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version           BIGINT         NOT NULL DEFAULT 1,
    status            TEXT           NOT NULL DEFAULT 'ACTIVE',
    PRIMARY KEY (id, accounting_period),
    -- 复式记账：一行恰好一边非零，不许两边都填或两边都空。
    CONSTRAINT finance_journal_entry_lines_one_sided
      CHECK ((debit > 0 AND credit = 0) OR (debit = 0 AND credit > 0))
) PARTITION BY LIST (accounting_period);
CREATE INDEX finance_journal_entry_lines_entry ON finance_journal_entry_lines (entry_id);
CREATE INDEX finance_journal_entry_lines_account ON finance_journal_entry_lines (account_id, accounting_period);

-- ⚠️ 与 inventory_movements 的月分区不同：会计期间不配后台自动建分区的
-- 任务（设计计划 §9 第 6 条）——开新的会计年度是一次业务动作，不是
-- 日历滚动窗口的必然事实。这里一次性建好 FY2026 全年 12 个月的分区，
-- 开 FY2027 时新增一份迁移文件，不是等后台任务自动建。
CREATE TABLE finance_journal_entry_lines_2026_01 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-01');
CREATE TABLE finance_journal_entry_lines_2026_02 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-02');
CREATE TABLE finance_journal_entry_lines_2026_03 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-03');
CREATE TABLE finance_journal_entry_lines_2026_04 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-04');
CREATE TABLE finance_journal_entry_lines_2026_05 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-05');
CREATE TABLE finance_journal_entry_lines_2026_06 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-06');
CREATE TABLE finance_journal_entry_lines_2026_07 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-07');
CREATE TABLE finance_journal_entry_lines_2026_08 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-08');
CREATE TABLE finance_journal_entry_lines_2026_09 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-09');
CREATE TABLE finance_journal_entry_lines_2026_10 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-10');
CREATE TABLE finance_journal_entry_lines_2026_11 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-11');
CREATE TABLE finance_journal_entry_lines_2026_12 PARTITION OF finance_journal_entry_lines FOR VALUES IN ('2026-12');
-- 同 erp-inventory 001 迁移的实测踩坑：建分区要求执行者是父表 owner，
-- 迁移用管理凭据跑，建出来的分区默认属于那个账号；本组件没有分区维护
-- 后台任务（上面刚说过），但归档任务（阶段三/§7）将来要用
-- erp_finance_rw 去 DETACH 已锁定期间的分区，同样需要先转移 owner。
ALTER TABLE finance_journal_entry_lines OWNER TO erp_finance_rw;

-- 应收台账，与总账分家（设计计划 §2.4）：可变，核销状态挂在这里，
-- 不用去改可能已经归档的总账分区。
CREATE TABLE ar_ledger (
    id                BIGSERIAL PRIMARY KEY,
    customer_id       TEXT           NOT NULL,  -- 不透明外键，来自 mdm-customer
    entry_id          BIGINT         NOT NULL REFERENCES finance_journal_entries (id),
    legal_entity_id   TEXT           NOT NULL,
    currency          TEXT           NOT NULL DEFAULT 'CNY',  -- 单币种（同上，阶段计划 Task 14 ④）
    amount            NUMERIC(18,2)  NOT NULL,
    reconciled_amount NUMERIC(18,2)  NOT NULL DEFAULT 0,
    -- §11.2.1 强制字段（status 复用为 OPEN/RECONCILED）
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version           BIGINT         NOT NULL DEFAULT 1,
    status            TEXT           NOT NULL DEFAULT 'OPEN',
    CONSTRAINT ar_ledger_amount_positive CHECK (amount > 0),
    CONSTRAINT ar_ledger_reconciled_bounds CHECK (reconciled_amount >= 0 AND reconciled_amount <= amount),
    CONSTRAINT ar_ledger_status_valid CHECK (status IN ('OPEN', 'RECONCILED'))
);
CREATE INDEX ar_ledger_customer ON ar_ledger (customer_id);

-- 应付台账。阶段二没有 erp-purchase（阶段六才有），这张表结构上归本
-- 组件（设计计划 §2），但暂时不会有任何写入——供应商侧的凭证消费留给
-- 阶段六。先建表是为了 legal_entity_id 数据权限列不用将来再补
-- （§11.2.1 的同一个判据）。
CREATE TABLE ap_ledger (
    id                BIGSERIAL PRIMARY KEY,
    supplier_id       TEXT           NOT NULL,
    entry_id          BIGINT         NOT NULL REFERENCES finance_journal_entries (id),
    legal_entity_id   TEXT           NOT NULL,
    currency          TEXT           NOT NULL DEFAULT 'CNY',  -- 单币种（同上，阶段计划 Task 14 ④）
    amount            NUMERIC(18,2)  NOT NULL,
    reconciled_amount NUMERIC(18,2)  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version           BIGINT         NOT NULL DEFAULT 1,
    status            TEXT           NOT NULL DEFAULT 'OPEN',
    CONSTRAINT ap_ledger_amount_positive CHECK (amount > 0),
    CONSTRAINT ap_ledger_reconciled_bounds CHECK (reconciled_amount >= 0 AND reconciled_amount <= amount),
    CONSTRAINT ap_ledger_status_valid CHECK (status IN ('OPEN', 'RECONCILED'))
);
CREATE INDEX ap_ledger_supplier ON ap_ledger (supplier_id);

-- 已用额度：本组件自己算的物化值（消费 sales.order.created.v1 累加，
-- 收款时冲减）。与下面的 customer_credit_snapshots 是两张表，不能合并
-- ——一个来自本组件自己的过账事实，一个来自外部事件摘要副本，更新
-- 时机完全不同（设计计划 §9 第 5 条）。
CREATE TABLE customer_credit_exposure (
    customer_id TEXT           PRIMARY KEY,
    currency    TEXT           NOT NULL DEFAULT 'CNY',  -- 单币种（同上，阶段计划 Task 14 ④）
    exposure    NUMERIC(18,2)  NOT NULL DEFAULT 0,
    -- §11.2.1 强制字段
    created_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version     BIGINT         NOT NULL DEFAULT 1,
    status      TEXT           NOT NULL DEFAULT 'ACTIVE',
    CONSTRAINT customer_credit_exposure_non_negative CHECK (exposure >= 0)
);

-- mdm-customer 的摘要副本：只取 credit_limit（额度值本身，设计计划 §5
-- 的三方分工——额度值在 mdm-customer，已用值在这里）。按 version 单调
-- 更新（§3.10）。
CREATE TABLE customer_credit_snapshots (
    customer_id  TEXT           PRIMARY KEY,
    credit_limit NUMERIC(18,2)  NOT NULL DEFAULT 0,
    -- §11.2.1 强制字段
    created_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version      BIGINT         NOT NULL DEFAULT 1,
    status       TEXT           NOT NULL DEFAULT 'ACTIVE'
);
