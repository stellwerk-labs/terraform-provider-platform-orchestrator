# Provider release channels

The release workflow runs after the reusable test workflow and only publishes
from `stellwerk-labs/terraform-provider-platform-orchestrator`. Checks run in a
fork are validation only. Maintainers must approve the exact source revision, tag
and destination before initiating a public release.

| Tag example | GitHub prerelease | GitHub latest |
| --- | --- | --- |
| `v2.0.0-rc.1` | Yes | No |
| `v2.0.0` | No | Yes, preserving the existing stable release behavior |

GoReleaser is pinned to `v2.18.1` in the release workflow. Its documented
[`prerelease: auto` and templated `make_latest` settings](https://goreleaser.com/customization/publish/scm/)
read the SemVer prerelease component. They do not inspect arbitrary tag
substrings. Build metadata alone keeps a stable version stable. RC and stable
releases both retain the same platform ZIP matrix, Terraform registry manifest,
SHA-256 checksums and detached GPG checksum signature. Signing still uses the
existing `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` secrets and imported fingerprint.

## Non-publishing checks

From the repository root, using the workflow's GoReleaser version:

```sh
goreleaser check
go test ./scripts
shellcheck scripts/verify-release-metadata.sh
make lint test
```

These commands neither create a tag nor publish a release. The Go tests validate
the actual release configuration, render the stable/RC templates and execute the
metadata validator with local JSON inputs. GitHub uploads, GPG signing and
Terraform registry ingestion are still release-environment checks.

## Before and after an authorized release

Before tagging, run the complete provider gates and real Terraform/OpenTofu
acceptance against the supported server revisions. Verify generated API pins,
upgrade documentation and the immutable Module ownership-transfer procedure.
Include reviewed release notes at `docs/releases/<tag>.md` in the tagged commit
for both candidates and stable releases. GoReleaser reads that exact file; a
missing file stops publication instead of producing empty or generated notes.
An RC is a separate version; do not rename it or reuse its tag for the eventual
stable release.

The workflow validates the published tag, draft/prerelease flags, asset count,
manifest, checksums and signature artifact. After an RC, also verify that GitHub's
`releases/latest` still points to the previous stable release. After a stable
release, verify that it points to the new stable tag. Validate the downloaded
checksum signature with the registered signing key and test installation with
an exact version constraint before announcing registry availability. A GitHub
prerelease alone does not prove registry indexing or a successful consumer
installation.

If publication or a post-publication check fails, inspect the existing release
and assets before retrying. Automatic recovery must not delete, overwrite or
retag published artifacts.
