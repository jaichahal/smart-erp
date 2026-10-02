-- +goose Up
-- Receivables subledger and the cash book the bank match reads.
-- A post-dated cheque is role pdc_in, never role bank, until it is deposited.

CREATE TABLE erp.ar_customers (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    name                text        NOT NULL,
    credit_limit_fils   bigint      NOT NULL DEFAULT 0 CHECK (credit_limit_fils >= 0),
    dunning_days        int[]       NOT NULL DEFAULT '{}',
    interest_enabled    boolean     NOT NULL DEFAULT false,
    annual_interest_bp  int         NOT NULL DEFAULT 0 CHECK (annual_interest_bp >= 0),
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.ar_invoices (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    customer_id         uuid        NOT NULL,
    number              text        NOT NULL,
    invoice_date        date        NOT NULL,
    due_date            date        NOT NULL,
    amount_fils         bigint      NOT NULL CHECK (amount_fils > 0),
    open_fils           bigint      NOT NULL CHECK (open_fils >= 0),
    pdc_covered_fils    bigint      NOT NULL DEFAULT 0 CHECK (pdc_covered_fils >= 0),
    bounced             boolean     NOT NULL DEFAULT false,
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, customer_id) REFERENCES erp.ar_customers(company_id, id),
    CHECK (open_fils + pdc_covered_fils <= amount_fils)
);

CREATE TABLE erp.ar_receipts (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    customer_id         uuid        NOT NULL,
    collector_id        text        NOT NULL DEFAULT '',
    method              text        NOT NULL CHECK (method IN ('cash', 'current_cheque', 'pdc', 'transfer', 'advance')),
    amount_fils         bigint      NOT NULL CHECK (amount_fils > 0),
    allocated_fils      bigint      NOT NULL DEFAULT 0 CHECK (allocated_fils >= 0 AND allocated_fils <= amount_fils),
    posted_on           date        NOT NULL,
    bank_account_id     uuid,
    cash_account_id     uuid,
    cheque_number       text        NOT NULL DEFAULT '',
    cheque_bank         text        NOT NULL DEFAULT '',
    cheque_date         date,
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, customer_id) REFERENCES erp.ar_customers(company_id, id)
);

CREATE TABLE erp.ar_allocations (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    receipt_id          uuid        NOT NULL,
    invoice_id          uuid        NOT NULL,
    amount_fils         bigint      NOT NULL CHECK (amount_fils > 0),
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, receipt_id) REFERENCES erp.ar_receipts(company_id, id),
    FOREIGN KEY (company_id, invoice_id) REFERENCES erp.ar_invoices(company_id, id)
);

CREATE TABLE erp.ar_customer_credits (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    customer_id         uuid        NOT NULL,
    credit_fils         bigint      NOT NULL DEFAULT 0 CHECK (credit_fils >= 0),
    PRIMARY KEY (company_id, customer_id),
    FOREIGN KEY (company_id, customer_id) REFERENCES erp.ar_customers(company_id, id)
);

CREATE TABLE erp.ar_party_lines (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    customer_id         uuid        NOT NULL,
    occurred_on         date        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN ('invoice', 'receipt', 'credit', 'interest')),
    doc_number          text        NOT NULL,
    debit_fils          bigint      NOT NULL DEFAULT 0 CHECK (debit_fils >= 0),
    credit_fils         bigint      NOT NULL DEFAULT 0 CHECK (credit_fils >= 0),
    FOREIGN KEY (company_id, customer_id) REFERENCES erp.ar_customers(company_id, id)
);

CREATE TABLE erp.bank_accounts (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    name                text        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN ('bank', 'cash', 'petty')),
    custodian_id        text        NOT NULL DEFAULT '',
    float_fils          bigint      NOT NULL DEFAULT 0 CHECK (float_fils >= 0),
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.ar_pdcs (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    direction           text        NOT NULL CHECK (direction IN ('received', 'issued')),
    status              text        NOT NULL CHECK (status IN ('in_hand', 'deposited', 'bounced', 'issued', 'presented')),
    receipt_id          uuid,
    customer_id         uuid,
    amount_fils         bigint      NOT NULL CHECK (amount_fils > 0),
    cheque_number       text        NOT NULL,
    cheque_bank         text        NOT NULL,
    cheque_date         date        NOT NULL,
    bank_account_id     uuid,
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, bank_account_id) REFERENCES erp.bank_accounts(company_id, id)
);

CREATE TABLE erp.ar_dunning (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    invoice_id          uuid        NOT NULL,
    days                int         NOT NULL CHECK (days > 0),
    sent_on             date        NOT NULL,
    PRIMARY KEY (company_id, invoice_id, days),
    FOREIGN KEY (company_id, invoice_id) REFERENCES erp.ar_invoices(company_id, id)
);

CREATE TABLE erp.ar_debit_notes (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    customer_id         uuid        NOT NULL,
    invoice_id          uuid        NOT NULL,
    amount_fils         bigint      NOT NULL CHECK (amount_fils >= 0),
    status              text        NOT NULL CHECK (status IN ('draft', 'issued')),
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, invoice_id) REFERENCES erp.ar_invoices(company_id, id)
);

CREATE TABLE erp.ar_settings (
    company_id          uuid        PRIMARY KEY REFERENCES erp.companies(id),
    concentration_bp    int         NOT NULL DEFAULT 10000 CHECK (concentration_bp >= 0 AND concentration_bp <= 10000)
);

CREATE TABLE erp.ar_shares (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    customer_id         uuid        NOT NULL,
    channel             text        NOT NULL CHECK (channel IN ('email', 'whatsapp')),
    pdf                 bytea       NOT NULL,
    FOREIGN KEY (company_id, customer_id) REFERENCES erp.ar_customers(company_id, id)
);

-- book_lines is the posting port until the ledger package owns journals.
-- role bank and role cash are spendable cash. role pdc_in is not.
CREATE TABLE erp.book_lines (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    role                text        NOT NULL CHECK (role IN (
        'receivable', 'bank', 'cash', 'pdc_in', 'pdc_out', 'customer_advances',
        'interest_income', 'expense', 'vat_input', 'petty_cash', 'payable', 'revenue', 'opening'
    )),
    account_id          uuid,
    debit_fils          bigint      NOT NULL DEFAULT 0 CHECK (debit_fils >= 0),
    credit_fils         bigint      NOT NULL DEFAULT 0 CHECK (credit_fils >= 0),
    doc_type            text        NOT NULL,
    doc_id              text        NOT NULL,
    party_id            uuid,
    posted_on           date        NOT NULL,
    CHECK (debit_fils = 0 OR credit_fils = 0),
    CHECK (debit_fils > 0 OR credit_fils > 0),
    FOREIGN KEY (company_id, account_id) REFERENCES erp.bank_accounts(company_id, id)
);
CREATE INDEX book_lines_role_idx ON erp.book_lines (company_id, role, posted_on);
CREATE INDEX book_lines_account_idx ON erp.book_lines (company_id, account_id, posted_on);

CREATE TABLE erp.bank_statement_lines (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    account_id          uuid        NOT NULL,
    bank_reference      text        NOT NULL,
    amount_fils         bigint      NOT NULL,
    booked_on           date        NOT NULL,
    description         text        NOT NULL DEFAULT '',
    status              text        NOT NULL CHECK (status IN ('open', 'proposed', 'matched', 'explained')),
    proposed_book_line_id uuid,
    matched_book_line_id  uuid,
    explain_reason      text        NOT NULL DEFAULT '',
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, account_id) REFERENCES erp.bank_accounts(company_id, id),
    UNIQUE (company_id, account_id, bank_reference)
);

CREATE TABLE erp.bank_match_rules (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    account_id          uuid        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN ('amount_and_date')),
    PRIMARY KEY (company_id, account_id),
    FOREIGN KEY (company_id, account_id) REFERENCES erp.bank_accounts(company_id, id)
);

CREATE TABLE erp.bank_feed (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    account_id          uuid        NOT NULL,
    provider            text        NOT NULL DEFAULT '',
    consent_expires     date,
    status              text        NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'failed')),
    message             text        NOT NULL DEFAULT '',
    alert               boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (company_id, account_id),
    FOREIGN KEY (company_id, account_id) REFERENCES erp.bank_accounts(company_id, id)
);

CREATE TABLE erp.bank_cheques (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    account_id          uuid        NOT NULL,
    number              bigint      NOT NULL CHECK (number > 0),
    state               text        NOT NULL CHECK (state IN ('issued', 'voided', 'cleared', 'stopped')),
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    UNIQUE (company_id, account_id, number),
    FOREIGN KEY (company_id, account_id) REFERENCES erp.bank_accounts(company_id, id)
);

CREATE TABLE erp.bank_facilities (
    company_id          uuid        NOT NULL REFERENCES erp.companies(id),
    id                  uuid        NOT NULL,
    name                text        NOT NULL,
    limit_fils          bigint      NOT NULL CHECK (limit_fils >= 0),
    utilisation_fils    bigint      NOT NULL DEFAULT 0 CHECK (utilisation_fils >= 0),
    annual_rate_bp      int         NOT NULL DEFAULT 0 CHECK (annual_rate_bp >= 0),
    maturity            date        NOT NULL,
    state_version       bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, id),
    CHECK (utilisation_fils <= limit_fils)
);

GRANT SELECT, INSERT, UPDATE ON
    erp.ar_customers, erp.ar_invoices, erp.ar_receipts, erp.ar_allocations,
    erp.ar_customer_credits, erp.ar_party_lines, erp.ar_pdcs, erp.ar_dunning,
    erp.ar_debit_notes, erp.ar_settings, erp.ar_shares, erp.book_lines,
    erp.bank_accounts, erp.bank_statement_lines, erp.bank_match_rules, erp.bank_feed,
    erp.bank_cheques, erp.bank_facilities
TO erp_app;

ALTER TABLE erp.ar_customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_customer_credits ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_party_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_pdcs ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_dunning ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_debit_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ar_shares ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.book_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_statement_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_match_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_feed ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_cheques ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bank_facilities ENABLE ROW LEVEL SECURITY;

CREATE POLICY ar_customers_tenant ON erp.ar_customers FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_invoices_tenant ON erp.ar_invoices FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_receipts_tenant ON erp.ar_receipts FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_allocations_tenant ON erp.ar_allocations FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_customer_credits_tenant ON erp.ar_customer_credits FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_party_lines_tenant ON erp.ar_party_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_pdcs_tenant ON erp.ar_pdcs FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_dunning_tenant ON erp.ar_dunning FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_debit_notes_tenant ON erp.ar_debit_notes FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_settings_tenant ON erp.ar_settings FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY ar_shares_tenant ON erp.ar_shares FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY book_lines_tenant ON erp.book_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_accounts_tenant ON erp.bank_accounts FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_statement_lines_tenant ON erp.bank_statement_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_match_rules_tenant ON erp.bank_match_rules FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_feed_tenant ON erp.bank_feed FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_cheques_tenant ON erp.bank_cheques FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY bank_facilities_tenant ON erp.bank_facilities FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose Down
DROP TABLE IF EXISTS erp.bank_facilities;
DROP TABLE IF EXISTS erp.bank_cheques;
DROP TABLE IF EXISTS erp.bank_feed;
DROP TABLE IF EXISTS erp.bank_match_rules;
DROP TABLE IF EXISTS erp.bank_statement_lines;
DROP TABLE IF EXISTS erp.book_lines;
DROP TABLE IF EXISTS erp.ar_shares;
DROP TABLE IF EXISTS erp.ar_settings;
DROP TABLE IF EXISTS erp.ar_debit_notes;
DROP TABLE IF EXISTS erp.ar_dunning;
DROP TABLE IF EXISTS erp.ar_pdcs;
DROP TABLE IF EXISTS erp.bank_accounts;
DROP TABLE IF EXISTS erp.ar_party_lines;
DROP TABLE IF EXISTS erp.ar_customer_credits;
DROP TABLE IF EXISTS erp.ar_allocations;
DROP TABLE IF EXISTS erp.ar_receipts;
DROP TABLE IF EXISTS erp.ar_invoices;
DROP TABLE IF EXISTS erp.ar_customers;
