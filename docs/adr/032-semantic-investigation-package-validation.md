# ADR-032 — Validação semântica de pacotes de investigação

## Status

Accepted

## Decision

`prodmap package verify` separa três propriedades. Integridade física cobre o
inventário fechado do ZIP, tamanhos, hashes e `SHA256SUMS`. Validade semântica
cobre JSON sem chaves duplicadas, versões e enums fechados, timestamps UTC,
intervalos half-open, números, referências, confidences e ausência de
causalidade. Autenticidade — assinatura, identidade do produtor e cadeia de
confiança — continua fora de escopo.

O parser rejeita chaves JSON duplicadas antes da decodificação tipada, inclusive
chaves equivalentes depois de unescape. O verificador reconstrói a comparação
sanitizada e chama o classificador real; portanto classificação, thresholds,
efeito, confidence, limitations e `classification_key` precisam corresponder ao
algoritmo atual. Também reconstrói a projeção canônica da investigação e exige
o `investigation_key` atual.

`comparison_key` v1 permanece um fingerprint opaco produzido upstream. O pacote
não serializa todos os parâmetros da query, como `min_samples` e
`min_coverage`; assim, o verificador valida formato e relações deriváveis, mas
não inventa defaults nem recalcula uma fórmula incompleta.

A verificação permanece totalmente offline: não carrega configuração, não abre
SQLite e não consulta rede. Esta decisão não altera thresholds, algoritmos,
migrations ou o formato v1, nem adiciona assinatura ou autenticidade externa.
