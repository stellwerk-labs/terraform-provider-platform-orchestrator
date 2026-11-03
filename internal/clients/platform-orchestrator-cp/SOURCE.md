# Orchestrator Control Plane API contract

`spec.yaml` is a byte-identical copy of the selected immutable public source
commit and the sole input of the official generated client.

- Repository: `stellwerk-labs/platform-orchestrator-cp`
- Commit: `3abef77d1ce1b4124576516c3e4f22f8f97ef9c8`
- File: `openapi/spec.yaml`
- Git blob: `f802f0d861e855b448e1d29bf0e7c1ddec1ff4bd`
- File SHA-256: `5b5e5382d9aa6ce9eed54d88431a20fb4b6d4e9672298f40fb449f608ad6c71a`

This additive contract exposes common `max_parallel_jobs` and
`supported_protocols` on every Runner configuration. Update omission preserves
Core settings; explicit null clears them. Existing clients may omit both fields.
The provider uses the repository's pinned official oapi-codegen generator without
hand-editing the spec or generated client.

The source commit is publicly reachable through the independently published
`shared/v3.1.0` SDK tag. That SDK publication does not release the corresponding
Core server behavior. A matching qualified Core release remains required before
releasing provider features that use these fields.

Before a client release, verify this immutable source through the unchanged
public-source checks without local spec overrides. Regenerate through the
repository's official generators; do not hand-edit the snapshot or client.
