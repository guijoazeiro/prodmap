# GitHub Actions deployment artifact source v1

Slice 3.4A fetches a logical GitHub Actions artifact and returns a validated
`deployment-ledger-jsonl/v1` snapshot. It does not expose a CLI command or
persist remote state.

The artifact is selected only when its name matches exactly, it is unexpired,
has a canonical `sha256:<64 lowercase hex>` digest, valid timestamps, a positive
bounded size, and a canonical workflow head SHA. Selection is newest
`created_at`, then greatest artifact ID. The ZIP transport digest and the ledger
`SourceHash` protect different bytes and must not be conflated.

The ZIP contains exactly `deployments.jsonl`, with the local ledger size limit.
Verified ledger revisions must match the workflow head SHA. Redirects are
bounded and never receive GitHub Authorization after crossing origin.
