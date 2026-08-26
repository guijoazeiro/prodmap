# Deployment ledger JSONL v1

Format identifier: `deployment-ledger-jsonl/v1`.

The input is UTF-8 JSONL: one non-empty JSON object per line, no comments,
duplicate keys, trailing JSON, or unknown fields. Required fields are
`schema_version`, `deployment_id`, `deployed_at`, `build_started_at`,
`build_date`, `environment`, `service`, `version`, `scenario_profile`,
`git_head`, `git_dirty`, `vcs_revision`, `vcs_revision_verified`,
`image_reference`, `image_id`, `repo_digest`, `compose_project`, and `status`.

`repo_digest` is the only nullable field. Timestamps are RFC3339 UTC and obey
`build_date <= build_started_at <= deployed_at`: the ledger's image creation date
may precede the deployment script start. `image_id` is an immutable
`sha256` or `sha512` identity. `repo_digest` is null or a repository-qualified
immutable digest. `image_reference` is validated but never used as identity.

`vcs_revision_verified=true` requires a clean repository and equal complete
`git_head`/`vcs_revision`; otherwise `vcs_revision` is `unknown`. Accepted status
values are `pending`, `running`, `succeeded`, `failed`, `cancelled`,
`rolled_back`, and `unknown`.

The canonical record fingerprint is `sha256-v1:<hex>` over semantic deployment,
time, verified-commit, and immutable-artifact claims. It excludes the source path,
ingestion time, JSON key order, and discarded presentation fields.
