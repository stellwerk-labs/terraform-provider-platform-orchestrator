# Terraform Provider Platform Orchestrator

This repo holds the source code for the Platform Orchestrator terraform provider. This allows organizations to configure all platform engineering concerns through infrastructure as code.

## License

Release archives include `licenses/`: the application's EUPL text, Go's
license/patent grants, and the union of dependency notices for every supported
configured GoReleaser target, including Windows, Darwin and FreeBSD. Collection
uses checksum-verified go-licenses v2.0.1 and Go 1.26.2, without changing the module's
minimum Go version. Unknown licenses and missing/empty legal notices fail the
build. Reciprocal-license source files saved by go-licenses are preserved too.
Generated notices are replaced only when their recorded checksums match and no
untracked files exist. Archive entry timestamps use the source commit date.

One mechanical classifier correction is restricted to the exact unreplaced
`github.com/oapi-codegen/nullable@v1.2.0`, its module checksum and original LICENSE
hash: the upstream file contains the Apache-2.0 copyright/header but not the full
license, and go-licenses excludes header-only matches. Its original bytes are
preserved alongside the hash-pinned full Apache license from the Apache Software
Foundation and a provenance record. Any changed identity/content or colliding
module prefix fails; other unknown licenses remain errors. This documents
packaging behavior, not a legal or compatibility certification.

Run `go test -count=1 ./scripts/licensecollector` for the real-classifier notice
and failure-boundary tests. The test installs the same verified tool into its
temporary directory if necessary. GoReleaser's before-hook generates notices;
the existing provider `-trimpath` and commit-based binary mtime remain enabled.

Licensed under the European Union Public Licence, version 1.2.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.23

## Building The Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command:

```shell
go install
```

## Adding Dependencies

This provider uses [Go modules](https://github.com/golang/go/wiki/Modules).
Please see the Go documentation for the most up to date information about using Go modules.

To add a new dependency `github.com/author/dependency` to your Terraform provider:

```shell
go get github.com/author/dependency
go mod tidy
```

Then commit the changes to `go.mod` and `go.sum`.

## Using the provider

After publication, the provider will be available as [stellwerk-labs/platform-orchestrator](https://registry.terraform.io/providers/stellwerk-labs/platform-orchestrator/latest) in the Terraform Registry and [stellwerk-labs/platform-orchestrator](https://search.opentofu.org/provider/stellwerk-labs/platform-orchestrator/latest) in the OpenTofu Registry.

```hcl
terraform {
  required_providers {
    platform-orchestrator = {
      source  = "stellwerk-labs/platform-orchestrator"
      version = "~> 1.1"
    }
  }
}

provider "platform-orchestrator" {
  org_id = "organization"
}
```

The provider also ships the marked Enterprise resource and data source
`platform-orchestrator_progressive_rollout` for the Stellwerk Enterprise plugin
Progressive Rollouts (`dev.stellwerk.progressive-rollouts`). Shipping them does
not install, enable or license the plugin. A platform operator enables an
installed plugin for the organization with
`platform-orchestrator_plugin_enablement` (the provider's `org_id` must be the
organization of the installation's service user) and finds its installation with
the `platform-orchestrator_plugin_installation` data source; installing,
activating, upgrading and removing plugins stay Plugin Manager operator
workflows (`octl plugin-manager`). Before any plugin work the provider
asks Core plugin discovery and then invokes only the advertised plugin
operations through Core. An unavailable plugin fails with one of the stable
codes `plugin_not_installed`, `plugin_not_enabled`, `plugin_not_entitled`,
`plugin_incompatible` or `plugin_unavailable`; a server without plugin
discovery reports `plugin_not_installed`. After a licence expires, refresh,
reads and the safety commands Core still advertises keep working, while new
forward changes fail with `plugin_not_entitled`. Core resources never depend on
the plugin.

Set `ca_file` (or `PO_CA_FILE`) to a PEM bundle of at most 4 MiB when the API
uses a private certificate authority. The provider appends it to the
operating-system trust roots and still requires normal hostname verification
and TLS 1.2 or newer.

The provider uses `https://api.stellwerk.localhost` by default. You can override it with the `api_url` provider attribute, the `PO_API_URL` environment variable, or an `octl` config file.

## Registry publication prerequisites

Terraform Registry publication requires a public GitHub repository named `terraform-provider-platform-orchestrator`, Terraform Registry access through a GitHub account with access to the `stellwerk-labs` organization, and an RSA or DSA GPG signing key registered in the Terraform Registry. The release workflow expects the private key and passphrase in `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` repository secrets.

OpenTofu Registry publication is a separate process. Submit the provider and signing key through the OpenTofu Registry GitHub issue forms after the GitHub repository and signed release are available.

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `make generate`.

In order to run the full suite of Acceptance tests, run `make testacc`.

*Note:* Acceptance tests create real resources, and often cost money to run.

They run against the organization in `PO_ORG_ID` with an organization Admin
token in `PO_AUTH_TOKEN`; `PO_API_URL` and `PO_CA_FILE` apply as for the
provider. `TestAccRunnerRuleResourceBasic` and `TestAccRunnerRuleDataSource`
create the organization's catch-all runner rule (no project, no environment
type), and Core keeps one rule per scope, so run them in an organization
without one.

```shell
make testacc
```

## Release-content verification

The pinned GoReleaser OSS SBOM hook inspects every configured archive before
checksums, signing or upload. It verifies the actual Go 1.26.2 toolchain,
architecture, compiled module graph and exact source-collected notice bytes,
then generates per-archive SPDX 2.3 using digest-pinned Syft 1.54.1. Producer
statements bind actual archive/SBOM hashes to the source tag, workflow revision
and run identity, without claiming a SLSA level or legal certification.

Gitleaks 8.30.1 and private-reference scans cover shipping paths, bounded archive
members, binary strings, release notes and generated metadata. Inline scanner
exemptions and shipped ignore files cannot weaken the gate. Required reciprocal
license source files are preserved. The public test fixtures in
[known-public-fixtures.json](scripts/known-public-fixtures.json) have exact
module/version/checksum, shipping path, complete-file hash and finding-count
proofs. Changed source, changed findings or added secrets fail. One additional
private-path heuristic exception covers the complete hashed public log-testing
source files, not arbitrary files from those modules. Secret detection remains
strict there.

The existing Registry contract remains unchanged: SHA-256 checksums include the
Registry manifest, archives, SPDX and producer metadata, and are signed with the
registered GPG key. The signer rechecks qualified bytes immediately before
signing, verifies the expected key fingerprint, then scans the resulting detached
signature. The passphrase is passed over stdin rather than a process argument.

Boundary tests require the checksum-verified tools from
`scripts/install-release-tools.sh`, Docker and the pinned Syft image in
`scripts/release-tools.sh`. They perform no publication and skip no required
scanner or cataloger behavior. Full cross-platform artifact and Registry
consumer qualification are separate release evidence.
