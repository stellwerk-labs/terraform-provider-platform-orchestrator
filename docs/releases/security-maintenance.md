# Terraform and OpenTofu provider security maintenance

Unreleased. These notes describe the local maintenance branch, not an available
artifact or a completed deployment.

## Security changes

Official builds use Go 1.26.10, including the certificate verification fix for
[CVE-2026-111943](https://pkg.go.dev/vuln/GO-2026-6809).

Existing HTTP, cryptography, OpenAPI and gRPC dependencies are updated to patched versions. Generated clients use the corrected server contracts. This maintenance does not change resource schemas, state ownership or the plugin entitlement boundary.

Normal CI now checks source dependencies with pinned govulncheck 1.9.0 and fails
on reachable known vulnerabilities. A passing source scan is not a guarantee
against unknown vulnerabilities or a substitute for deployment qualification.

The contained acceptance job selects every `TestAcc...Local` case, including
all-organization plugin enablement. A source-inventory regression test prevents
silently omitting new local cases or selecting credential-backed tests there.
The separate full server-backed acceptance job still requires its disposable
organization and credentials; contained tests do not replace it.

## Compatibility and release checks

Existing published versions and artifacts remain immutable. Use a new patch
version after the normal integration and artifact checks pass. Source builds
following the supported release tooling require Go 1.26.10. Clients using the
corrected vendored API documents must verify their exact published server source
pins before release.
