-- erp/finance: the tables of the whole component (one migration order for all its modules).

-- One queued realignment of the department projection at a time (internal/orgunits): an event of a newer mdm/org
-- epoch waits for it (protocol 02).
CREATE TABLE org_realign (
    single    boolean PRIMARY KEY DEFAULT true CHECK (single),
    queued_at timestamptz NOT NULL DEFAULT now()
);

-- Every incremental fact this component published, by its ID: what FactService.Read answers (protocol 05, common
-- rules: a consumer that missed one reads it back).
CREATE TABLE finance_fact (
    fact_id     uuid NOT NULL,
    type        text NOT NULL,
    data        jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (fact_id, type)
);

-- The chart of accounts per legal entity: a heading (not postable) has accounts below it; vouchers name postable
-- active leaves only (VC01).
CREATE TABLE account (
    id              uuid PRIMARY KEY,
    legal_entity_id uuid NOT NULL,
    code            text NOT NULL,
    name            text NOT NULL,
    category        text NOT NULL CHECK (category IN ('asset', 'liability', 'equity', 'income', 'expense')),
    parent_id       uuid REFERENCES account (id),
    postable        boolean NOT NULL,
    state           text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'inactive')),
    search          text NOT NULL DEFAULT '',
    search_version  int NOT NULL DEFAULT 0,
    version         bigint NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (legal_entity_id, code)
);

-- A legal entity's mapping of a posting rule's roles to its accounts (the rules are the component's catalog,
-- internal/ledger); the version counts the whole mapping's changes (0: never set).
CREATE TABLE account_mapping (
    legal_entity_id uuid NOT NULL,
    rule_key        text NOT NULL,
    role            text NOT NULL,
    account_code    text NOT NULL,
    PRIMARY KEY (legal_entity_id, rule_key, role)
);
CREATE TABLE mapping_version (
    legal_entity_id uuid PRIMARY KEY,
    version         bigint NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Accounting periods by month: made open the first time something posts to them (PD01).
CREATE TABLE period (
    legal_entity_id uuid NOT NULL,
    period          text NOT NULL CHECK (period ~ '^[0-9]{4}-[0-9]{2}$'),
    state           text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'reopened')),
    revision        int NOT NULL DEFAULT 1,
    reason          text,
    version         bigint NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (legal_entity_id, period)
);

-- Vouchers: a system voucher is posted from a fact; a manual one is a draft until approved (VC01-VC04). The number is
-- given when posted, continuous per legal entity and period (VC03).
CREATE TABLE voucher (
    id                  uuid PRIMARY KEY,
    legal_entity_id     uuid NOT NULL,
    number              text UNIQUE,
    period              text NOT NULL,
    business_date       date NOT NULL,
    posting_date        date NOT NULL,
    description         text NOT NULL,
    source_kind         text NOT NULL,
    source_id           text,
    state               text NOT NULL CHECK (state IN ('draft', 'submitted', 'posted', 'reversed')),
    maker_id            text,
    approver_id         text,
    reason              text,
    reverses_voucher_id uuid REFERENCES voucher (id),
    reversed_by_id      uuid REFERENCES voucher (id),
    search              text NOT NULL DEFAULT '',
    search_version      int NOT NULL DEFAULT 0,
    version             bigint NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX voucher_list ON voucher (created_at DESC, id);
CREATE INDEX voucher_period ON voucher (legal_entity_id, period, state);
CREATE UNIQUE INDEX voucher_source ON voucher (source_kind, source_id) WHERE source_kind <> 'manual' AND source_id IS NOT NULL;

-- A voucher line: the amount in the transaction currency and in the legal entity's base currency.
CREATE TABLE voucher_line (
    id            uuid PRIMARY KEY,
    voucher_id    uuid NOT NULL REFERENCES voucher (id),
    line_no       int NOT NULL,
    account_code  text NOT NULL,
    side          text NOT NULL CHECK (side IN ('debit', 'credit')),
    currency      text NOT NULL,
    amount        numeric(19,4) NOT NULL CHECK (amount >= 0),
    base_amount   numeric(19,4) NOT NULL CHECK (base_amount >= 0),
    partner_id    uuid,
    department_id uuid,
    UNIQUE (voucher_id, line_no)
);

-- The numbers of a continuous series a full recovery raised the series over (P14.5): registered void.
CREATE TABLE voided_number (
    legal_entity_id uuid NOT NULL,
    series          text NOT NULL,
    period          text NOT NULL,
    number          bigint NOT NULL,
    reason          text NOT NULL,
    PRIMARY KEY (legal_entity_id, series, period, number)
);
