# Progressive Rollouts plugin API models

`spec.yaml` is a byte-identical copy of the canonical client contract
`progressive-rollouts-v1alpha.openapi.yaml` of the Stellwerk Enterprise plugin
Progressive Rollouts (`dev.stellwerk.progressive-rollouts`), contract level
`v1alpha`. The plugin owns the contract; `octl` vendors the same bytes as
`clients/progressive-rollouts/spec.yaml` in `platform-orchestrator-cli`.

- `spec.yaml` SHA-256: `b5f53a7717c3280dd62f6c9624bcb316c9396e17256b928be0ce7633a9da886d`

The plugin implementation is commercial and is not part of this repository.
Only its client contract is vendored, because the standard provider ships the
marked Enterprise resources next to the Core resources. Shipping them does not
install, enable or entitle the plugin.

The plugin publishes no client routes, so `paths` is empty. Each operation is an
OpenAPI Operation Object in `x-stellwerk-operations` with `x-method`,
`x-mutating`, `x-required-feature` and, where relevant, `x-safety-path-values`.
The provider asks Core plugin discovery (`getPluginCapability`,
`listPluginContributions`) and then invokes an advertised operation by its
`operationId` through the Core `invokePluginOperation` envelope. Discovery, the
envelope and the availability errors are defined by the Core API only.
`ModuleVersionSemanticStatus` and `ModuleVersionPinStatus` are marked mirrors of
Core value sets; Core stays authoritative.

The generated package contains models only, with `skip-prune` because no schema
is referenced from `paths`. Regenerate with
`go generate ./internal/clients/progressive-rollouts` (part of `make generate`).
Never edit `spec.yaml` by hand: vendor a new canonical revision byte-identically
and update the digest here and in `internal/provider/api_contract_test.go` in
the same change. Any revision requires regeneration while the level is
`v1alpha`.

Feature branch qualification uses the local candidate from `spike/plugin-pipelines-20261118`, based on qualified Rollouts `aff263b`. This candidate is not a published or released plugin contract. Its canonical owner is `rollouts/api/progressive-rollouts-v1alpha.openapi.yaml`; release requires coordinated qualification and publication.
