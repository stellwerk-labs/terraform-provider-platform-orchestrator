# Core contract

`spec.yaml` is a byte-identical copy of the Control Plane OpenAPI document and
the only input of the generated `cp` client.

- Repository: `stellwerk-labs/platform-orchestrator-cp`
- Commit: `d323f9b7ed2c81913856b55294ab92ece6b38b03`
- File: `openapi/spec.yaml`
- Git blob: `33976244f6045b196891383262ea01eb604bb777`
- File SHA-256: `1cab2e88d1132b77d2865e43e8ddecdd44437a71146c788d168d5cc1245e39b3`

The pinned commit contains the Control Plane plugin foundation (Plugin Manager
operator API, platform principals as identities, several organizations per
installation, organization mode, installation-wide licence grants). To repin,
copy `openapi/spec.yaml` from the published commit unchanged, update commit,
blob and SHA-256 here, then run `make generate` and `make check-api-specs`.

Never edit `spec.yaml` or the generated client by hand. Plugin APIs have their
own contracts, for example [Progressive Rollouts](../progressive-rollouts/SOURCE.md).

Publication status: this security source commit is prepared locally and is not
published yet. Public-source verification must pass after the coordinated
server-source publication, before this client is released.
