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

-- Received facts (protocol 05, common rules): the dedup record by fact ID and content digest, written in the
-- transaction that received the event; the post-fact work item posts it outside that transaction (it takes evidence
-- from master data first) and marks it posted in the posting transaction (internal/intake).
CREATE TABLE fact_received (
    fact_id     uuid PRIMARY KEY,
    type        text NOT NULL,
    source      text NOT NULL,
    digest      text NOT NULL,
    data        jsonb NOT NULL,
    state       text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'posted')),
    voucher_id  uuid REFERENCES voucher (id),
    received_at timestamptz NOT NULL DEFAULT now(),
    posted_at   timestamptz
);
CREATE INDEX fact_received_pending ON fact_received (received_at) WHERE state = 'pending';

-- Open items: receivables, payables, a customer's credit balance; the available amount is amount - settled -
-- pay_claimed (protocol 05). No state column for open or settled: the open balance says it; state is a manual item's
-- approval (pending_approval, cancelled) or open.
CREATE TABLE open_item (
    id              uuid PRIMARY KEY,
    legal_entity_id uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('receivable', 'payable', 'credit_balance')),
    partner_id      uuid NOT NULL,
    currency        text NOT NULL,
    amount          numeric(19,4) NOT NULL CHECK (amount >= 0),
    settled         numeric(19,4) NOT NULL DEFAULT 0 CHECK (settled >= 0),
    pay_claimed     numeric(19,4) NOT NULL DEFAULT 0 CHECK (pay_claimed >= 0),
    due_date        date NOT NULL,
    origin          text NOT NULL CHECK (origin IN ('invoice', 'supplier_invoice', 'manual', 'fund')),
    source_kind     text NOT NULL,
    source_id       text,
    source_number   text,
    state           text NOT NULL DEFAULT 'open' CHECK (state IN ('pending_approval', 'open', 'cancelled')),
    department_id   uuid,
    department_path text,
    voucher_id      uuid REFERENCES voucher (id),
    -- a manual open item's (MI01): who registered it, why, the other side of its voucher
    maker_id             text,
    description          text,
    counter_account_code text,
    search          text NOT NULL DEFAULT '',
    search_version  int NOT NULL DEFAULT 0,
    version         bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    is_open         boolean GENERATED ALWAYS AS (state = 'open' AND amount > settled) STORED,
    CHECK (settled + pay_claimed <= amount)
);
CREATE UNIQUE INDEX open_item_source ON open_item (origin, source_id) WHERE source_id IS NOT NULL AND origin <> 'manual';
CREATE INDEX open_item_list ON open_item (created_at DESC, id);
CREATE INDEX open_item_partner ON open_item (legal_entity_id, partner_id, kind, due_date);

-- Supplier invoices (docs/design.md, the supplier invoice table; SI01-SI05): an order invoice (every line an order
-- line, matched) or an expense invoice (every line an expense account, approved). department_path is the org-tree
-- fact of security 07, copied from the department.
CREATE TABLE supplier_invoice (
    id                     uuid PRIMARY KEY,
    number                 text NOT NULL UNIQUE,
    legal_entity_id        uuid NOT NULL,
    supplier_id            uuid NOT NULL,
    supplier_number        text NOT NULL,
    invoice_date           date NOT NULL,
    due_date               date NOT NULL,
    currency               text NOT NULL,
    kind                   text NOT NULL CHECK (kind IN ('order', 'expense')),
    state                  text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'matched', 'mismatch', 'posted', 'cancelled')),
    maker_id               text NOT NULL,
    difference_approved_by text,
    difference_reason      text,
    open_item_id           uuid REFERENCES open_item (id),
    voucher_id             uuid REFERENCES voucher (id),
    department_id          uuid,
    department_path        text,
    search                 text NOT NULL DEFAULT '',
    search_version         int NOT NULL DEFAULT 0,
    version                bigint NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX supplier_invoice_supplier_number ON supplier_invoice (legal_entity_id, supplier_id, supplier_number)
    WHERE state <> 'cancelled';
CREATE INDEX supplier_invoice_list ON supplier_invoice (created_at DESC, id);

-- A supplier invoice line: an order line's quantity in the order line's unit, or an expense account; match is the
-- last matching's result (SI02), null before one.
CREATE TABLE supplier_invoice_line (
    id                   uuid PRIMARY KEY,
    invoice_id           uuid NOT NULL REFERENCES supplier_invoice (id),
    line_no              int NOT NULL,
    order_line_id        uuid,
    expense_account_code text,
    description          text,
    quantity             numeric(19,6) NOT NULL CHECK (quantity > 0),
    unit_price           numeric(19,6) NOT NULL CHECK (unit_price >= 0),
    net                  numeric(19,4) NOT NULL,
    tax                  numeric(19,4) NOT NULL CHECK (tax >= 0),
    match                jsonb,
    UNIQUE (invoice_id, line_no),
    CHECK ((order_line_id IS NULL) <> (expense_account_code IS NULL))
);

-- What posted supplier invoices took of an order line (its unit and the base unit) and of a receipt movement (base
-- unit): the next invoice's invoiceable quantity is net of it (SI02, SI03).
CREATE TABLE invoiced_line (
    order_line_id uuid PRIMARY KEY,
    quantity      numeric(19,6) NOT NULL,
    base_quantity numeric(19,6) NOT NULL
);
CREATE TABLE invoiced_movement (
    movement_id   uuid PRIMARY KEY,
    base_quantity numeric(19,6) NOT NULL
);

-- Payment claims (docs/design.md, the claim table; protocol 05's claim pattern, the payment use): an open item's
-- pay_claimed is the sum of its pending and confirmed claims. claim_id is the claimant's; expires_at is a pending
-- claim's.
CREATE TABLE pay_claim (
    claim_id          text PRIMARY KEY,
    open_item_id      uuid NOT NULL REFERENCES open_item (id),
    claimant          text NOT NULL,
    amount            numeric(19,4) NOT NULL CHECK (amount > 0),
    state             text NOT NULL CHECK (state IN ('pending', 'confirmed', 'formalized', 'expired', 'released')),
    expires_at        timestamptz,
    formalized_amount numeric(19,4) NOT NULL DEFAULT 0,
    reversed_amount   numeric(19,4) NOT NULL DEFAULT 0,
    version           bigint NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pay_claim_open_item ON pay_claim (open_item_id);

-- Allocations (docs/design.md, ST03-ST05): money of a fund fact on an open item, or a reversal of one (the original
-- kept); claim_id when the money used a payment claim.
CREATE TABLE allocation (
    id                     uuid PRIMARY KEY,
    fund_fact_id           uuid NOT NULL,
    open_item_id           uuid NOT NULL REFERENCES open_item (id),
    amount                 numeric(19,4) NOT NULL CHECK (amount > 0),
    direction              text NOT NULL CHECK (direction IN ('settle', 'reverse')),
    claim_id               text REFERENCES pay_claim (claim_id),
    reverses_allocation_id uuid UNIQUE REFERENCES allocation (id),
    created_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX allocation_open_item ON allocation (open_item_id, created_at);
CREATE INDEX allocation_fund ON allocation (fund_fact_id);
