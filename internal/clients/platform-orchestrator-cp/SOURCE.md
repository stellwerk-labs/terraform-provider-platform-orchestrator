# Core contract

`spec.yaml` is a byte-identical copy of the Control Plane OpenAPI document and
the only input of the generated `cp` client.

- Repository: `stellwerk-labs/platform-orchestrator-cp`
- Commit: `fc8059255bd16ff61b0e4d3db122efd34912d068`
- File: `openapi/spec.yaml`
- Git blob: `b8ddf111b54f363169798121e0ce1526f627cfe0`
- File SHA-256: `903b836a6acd02aa24aa0f9adb41093a65df7e1be8358d9f11baf1b2de906022`

The published commit contains the Control Plane plugin foundation (Plugin Manager
operator API, platform principals as identities, several organizations per
installation, organization mode, installation-wide licence grants). To repin,
copy `openapi/spec.yaml` from the published commit unchanged, update commit,
blob and SHA-256 here, then run `make generate` and `make check-api-specs`.

Never edit `spec.yaml` or the generated client by hand. Plugin APIs have their
own contracts, for example [Progressive Rollouts](../progressive-rollouts/SOURCE.md).
