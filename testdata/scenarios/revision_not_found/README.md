# Declared revision unavailable locally

Fixture canônica declarativa da Phase 1. IDs, hashes, digests, hosts e timestamps são sintéticos e não representam resultados reais.

## Razão esperada

A syntactically valid OCI revision that is absent from the frozen repository produces UNKNOWN and an explicit warning.

## Arquivos

- `input.json`: fatos congelados das fontes, na ordem de chegada declarada.
- `config.json`: relógio fixo e allowlist de labels OCI.
- `expected.json`: invariantes esperados de persistência e correlação.
- `expected-cli.json`: contrato esperado da saída JSON do comando.

O relógio é fixado em `2026-08-19T12:00:00Z`. Nenhum segredo real é usado.
