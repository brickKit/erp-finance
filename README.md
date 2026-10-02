[English](README.md) · [中文](README.zh.md)

# erp/finance

Chart of accounts, general ledger entries, receivable and payable ledgers, period locks and reconciliation.

## Use it in a project

```bash
brickkit add erp/finance@2.0.0
brickkit up
```

Prepare first: see "Before you deploy" in BRICKKIT.md (a PostgreSQL schema and login role, NATS, the authorization and JWKS URLs, and granting legal-entity access before anyone sees any data).

## Documentation

| To find out | Read |
|---|---|
| What it does and does not do, how to configure it, what to prepare | [BRICKKIT.md](BRICKKIT.md) |
| Its gRPC, REST and event contracts | [contracts/](contracts/) |
| Why it is designed this way: boundary, posting and idempotency, periods, receivables and aging, open questions | [docs/design.md](docs/design.md) |
| What it depends on (nothing) and its configuration keys | [component.yaml](component.yaml) |
| How to develop it: code map, tests, pitfalls | [AGENTS.md](AGENTS.md) |

## Development

Go 1.25, Gin, `database/sql` with pgx, golang-migrate through be-sdk-go. Tests need a real PostgreSQL (`TEST_PG_DSN`) and NATS (`TEST_NATS_URL`); the commands and what success looks like are in AGENTS.md, section "Build and test". Inside the BrickEnterprise project, `make verify ID=erp/finance FOCUS=1 SEED=1` at the project root runs it for real, in a container and as a local process, and tears down afterwards.
