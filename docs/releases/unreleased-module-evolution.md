# Terraform/OpenTofu Module authoring

Unreleased. Requires matching server APIs and gateway routes.

Adds editable `module_draft` authoring, retained revisions and a separate
publication step. A stable `authoring_key` makes creation retry-safe. Destroy
closes an open Draft and retains its history.

Adds immutable scoped `module_draft_test` capture and explicit deployment
`draft_test_snapshots`. Editing a Draft preserves its identity, earlier captures
and deployed output. Explicitly reviewing a later revision captures and deploys
new material. No-op plans remain empty after capture, execution and publication.

Resource Type interfaces use immutable consecutive version identities, separate
from catalogue concurrency revisions. New data sources expose exact interfaces
and history. Module Version import preserves a later selected interface.

Real Terraform/OpenTofu execution and Console workflows pass against actual
CP/IAM/DP/Runner services. A seeded server upgrade also passes with native
binaries. Final public API-source pins, packaged upgrade and the complete
plugin-compatible release set remain gates before versioning/publication.
Existing immutable Versions and provider ownership remain intact.
