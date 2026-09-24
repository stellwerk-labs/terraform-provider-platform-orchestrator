---
page_title: "Upgrading to Orchestrator Module Version Management"
subcategory: ""
description: |-
  Migrate mutable Module ownership, publish immutable Versions and manage retained history safely.
---

# Upgrading to Orchestrator Module Version Management

This release introduces a breaking change to Module authoring. You must migrate
existing mutable Module configuration yourself. Existing infrastructure and historical Module
identities remain in Stellwerk. Back up and protect Terraform/OpenTofu state
before changing ownership, review the plan, and keep the legacy Module intact
during migration.

The Orchestrator manages Module identities, immutable Versions and Pins.
Use a compatible server providing the Module catalogue, immutable Version and
Pin APIs. This provider does not invent SemVer or artifact digests for old data.

## Separate catalogue and executable Version ownership

- `platform-orchestrator_module_catalogue_entry` owns the stable slug, display
  name, description, tags and catalogue status.
- `platform-orchestrator_module_version` publishes a complete immutable
  executable definition. Every publication starts Proposed and Unverified.
- Explicit lifecycle commands promote, deprecate, mark Defective or restore the
  permitted predecessor. Verification is not required to use a Version.
- Resource Types are immutable contracts. Changing one requires a new identity,
  because the old ID is never updated or replaced. Archive is reversible.

The complete initial-publication example is in
`examples/module-management/main.tf` in the provider repository.

## Transfer an existing legacy Module

1. Record the existing Module slug, Resource Type and catalogue metadata. Retain
   a protected state backup. Do not rewrite historical Version UUIDs or opaque
   v0 identifiers.
2. Replace the legacy `platform-orchestrator_module` configuration with a
   `platform-orchestrator_module_catalogue_entry` block using the same slug and
   Resource Type. Initially match the current metadata/status.
3. Remove only the old Terraform state address, then import the existing
   catalogue identity into the new resource. These commands leave the remote
   Module in place:

```shell
terraform state rm platform-orchestrator_module.service
terraform import platform-orchestrator_module_catalogue_entry.service service
terraform plan
```

Use `tofu` instead of `terraform` for OpenTofu. Ensure only one resource manages
the catalogue identity. The plan must keep the remote Module and its Resource
Type in place.

4. Historical v0 remains readable in Stellwerk. Keep its original identifier
   instead of fabricating a SemVer or importing it as a newly published managed
   Version. When intentionally changing executable configuration, publish a
   complete new SemVer definition. The first promoted stable managed version
   becomes migration generation v1.

The deprecated legacy resource remains readable/importable during this handoff.
The Orchestrator rejects its old mutable writes with a migration diagnostic.

## Immutable publication and explicit lifecycle

A Version definition supplies all fixed inputs, parameter declarations,
provider mappings, dependencies, co-provisioned resources and module source.
Use a fresh SemVer for changed executable content, never edit an existing
publication. Inline source omits `artifact_digest`. Referenced artifacts require
an exact `source_revision`; their canonical SHA-256 `artifact_digest` is optional.
When supplied, the digest is immutable and protects the external artifact, not
the entire Orchestrator definition. An omitted digest does not mean the artifact
is verified.
Version metadata reads retain the stable `artifact_digest: ""` sentinel for an
absent claim. The provider omits that sentinel when reconstructing an imported
publication definition, preserving the distinction between absence and a claim.

Resource Types may define an immutable `module_contract`, a bounded OpenAPI 3.0
Schema Object over fixed inputs, parameters, provider mappings, dependencies,
co-provisioned resources and declared outputs. This is offline validation of
the author-declared interface. The Orchestrator does not inspect an external artifact.
Omitted contracts impose no additional restrictions on existing Resource Types.

Every new managed publication bound to a nonempty Resource Type `output_schema`
must include an exactly matching `output_schema` in its `definition`. Object
key order and whitespace are ignored; array order is significant. Historical
versions remain unchanged, without invented output declarations. The provider
reports unknown definition fields as errors.

Only one Proposed version may exist per Module. Prereleases cannot become
Default. Stable graduation publishes a separate stable Proposed Version and
deprecates the exact prerelease atomically. Default promotion must advance the
permitted lineage; restoration is restricted to the allowed predecessor.

Set `lifecycle_status` only if this configuration intentionally owns that
lifecycle. Omit it before delegating later promotion to another client; otherwise
Terraform may attempt to reconcile the configured status. Lifecycle transaction
resources are immutable receipts. Terraform records the command result once.
Their expected revisions are reviewed snapshots. Referencing mutable resulting
`resource_version` attributes would cause repeated plans.

Published Versions can be adopted without republication:

```shell
terraform import platform-orchestrator_module_version.release service/1.0.0
```

The second component may also be the actual Version UUID. Match the complete
published definition in configuration and review the resulting no-op plan.

## Teardown retains history

Destroying a Version removes its Terraform state reference only. Stellwerk
permanently retains the publication and lifecycle history.

Destroying a catalogue entry first attempts normal empty-shell deletion. Only
the structured `module_history_retained` conflict triggers a fresh read followed
by revision-checked archival. Other conflicts, active reservations and permission
failures remain errors. `deletion_reason` provides the archival audit context.
Archive leaves infrastructure untouched.

Referenced Provider and Resource Type identities must also remain. Set
`deletion_policy = "retain"` explicitly on those dependencies to relinquish
Terraform ownership without modifying them. The default is `delete`, which
requests API deletion and reports reference conflicts. Retention leaves Provider
archival and stored configuration unchanged.

Import retained resources before managing them again. Provider import IDs use
`provider-type.provider-id`; Resource Type and catalogue imports use their IDs.
Destroying immutable operation receipts never reverses the underlying commands.

## Exact Pins and notes

Pins protect exact effective Environment versions, including currently active
Deprecated versions. Pin UUIDs are importable into
`platform-orchestrator_module_version_pin`. Destroy performs an explicit audited
Unpin and requires the current scoped Unpin permission, not ownership by
the original creator or an add-on override permission.

Bulk Pin/Unpin/Discard resources operate on explicit frozen Environment UUIDs.
They do not use inherited selectors, and later Environments do not inherit a
previous snapshot. Append-only Pin notes do not change version, protection or
activation boundary.
Deleting a note resource removes only its Terraform receipt, not the audit note.

## Partial failure and recovery

If publication succeeds but a requested promotion fails, the provider saves the
immutable Version identity to state before returning the error. Correct the
lifecycle input or permission and retry; do not recreate the Version under a new
identity to hide the failure. The same applies to catalogue creation followed by
failed archival. Investigate unexpected missing immutable Versions; do not
silently recreate history.

## Local client acceptance

Build the provider locally and configure a disposable loopback Orchestrator API through
`PO_API_URL`, `PO_ORG_ID` and `PO_AUTH_TOKEN`, without printing or committing the
token. The acceptance helper exercises real Terraform or OpenTofu, including
publication, lifecycle changes, retained teardown, import and no-op plans:

```shell
bash scripts/test-module-management-clients.sh terraform /absolute/path/terraform-provider-platform-orchestrator catalogue
bash scripts/test-module-management-clients.sh tofu /absolute/path/terraform-provider-platform-orchestrator catalogue-external
```

`catalogue-external` also proves publication and import with an omitted external
digest. Both cases validate declared output and Resource Type contract retention.
These publication fixtures do not execute infrastructure or claim to verify the
external artifact. Protected temporary state and command logs are retained for
inspection. Only new, uniquely named catalogue records are created; no database
reset occurs.
