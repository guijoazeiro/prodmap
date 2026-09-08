# ADR-028 — Pacote de investigação reproduzível

## Status

Accepted

## Decision

`prodmap package create` produz um ZIP portátil a partir exclusivamente da
projeção já sanitizada de `prodmap investigate`. O formato
`prodmap-investigation-package/v1` contém exatamente `manifest.json`,
`investigation.json` e `SHA256SUMS`.

O manifest registra versão, instante UTC RFC3339Nano, chave da investigação,
perfil `safe-default/v1`, ausência de causalidade e inventário com tamanho,
media type e SHA-256 de `investigation.json`. `SHA256SUMS` lista os hashes de
`investigation.json` e `manifest.json` em ordem lexicográfica.

Cada entrada é limitada a 4 MiB e o conteúdo descompactado total a 8 MiB. A
criação usa arquivo temporário no diretório de destino, `fsync`, publicação
atômica sem substituição, `fsync` do diretório e permissão final 0600.

`prodmap package verify` é offline: não carrega configuração nem SQLite. Ele
aceita somente os três arquivos regulares esperados, valida JSON estrito,
inventário, checksums, limites e o scanner de redaction. A validade semântica
(versões, enums, relações analíticas, referências e chaves reproduzíveis) é
definida em [ADR-032](032-semantic-investigation-package-validation.md).
Conteúdo bruto, credenciais, paths absolutos e identidades proibidas são
recusados.

“Reproduzível” significa autocontido e verificável contra seu conteúdo
sanitizado. Não significa assinatura, proveniência autenticada ou autenticidade
criptográfica de fontes externas.
