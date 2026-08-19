# Out-of-order ingestion

Fixture canônica declarativa da Phase 1. IDs, hashes, digests, hosts e timestamps são sintéticos e não representam resultados reais.

## Razão esperada

Arrival order differs from observation time; both historical records remain and the latest-by-observed_at record is selected.

## Arquivos

- `input.json`: fatos congelados das fontes, na ordem de chegada declarada.
- `config.json`: relógio fixo e allowlist de labels OCI.
- `expected.json`: invariantes esperados de persistência e correlação.
- `expected-cli.json`: contrato esperado da saída JSON do comando.

O relógio é fixado em `2026-08-19T12:00:00Z`. Nenhum segredo real é usado.
