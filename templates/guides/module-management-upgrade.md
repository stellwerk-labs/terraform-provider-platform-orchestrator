---
page_title: "Upgrading to Core Module Version Management"
subcategory: ""
description: |-
  Migrate mutable Module ownership, publish immutable Versions and manage retained history safely.
---

# Upgrading to Core Module Version Management

This is a breaking authoring change, not an automatic conversion of mutable
Module configuration. Existing infrastructure and historical Module identities
remain in Stellwerk. Back up and protect Terraform/OpenTofu state before changing
ownership, review the plan, and do not destroy a legacy Module to migrate it.

Module management is Core functionality and needs no orchestration add-on.
Use a compatible server providing the Module catalogue, immutable Version and
Pin APIs. This provider does not invent SemVer or artifact digests for old data.

## Separate catalogue and executable Version ownership

- `platform-orchestrator_module_catalogue_entry` owns the stable slug, display
  name, description, tags and catalogue status.
- `platform-orchestrator_module_version` publishes a complete immutable
  executable definition. Every publication starts Proposed and Unverified.
- Explicit lifecycle commands promote, deprecate, mark Defective or restore the
  permitted predecessor. Verification is not a prerequisite for use.
- Resource Types are immutable contracts. Changing one requires a new identity,
  not an update or replacement under the old ID. Archive is reversible.

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
   catalogue identity into the new resource. These commands do not call remote
   Module deletion:

```shell
terraform state rm platform-orchestrator_module.service
terraform import platform-orchestrator_module_catalogue_entry.service service
terraform plan
```

Use `tofu` instead of `terraform` for OpenTofu. Do not leave both resources
managing the same catalogue identity. A plan must not propose replacement of the
remote Module or its Resource Type.

4. Historical v0 remains readable in Stellwerk. Do not fabricate a SemVer or
   import it as a newly published managed Version. When intentionally changing
   executable configuration, publish a complete new SemVer definition. The
   first promoted stable managed version becomes migration generation v1.

The deprecated legacy resource remains readable/importable during this handoff.
Its old mutable writes are rejected by managed Core with a migration diagnostic.

## Immutable publication and explicit lifecycle

A Version definition supplies all fixed inputs, parameter declarations,
provider mappings, dependencies, co-provisioned resources and module source.
Use a fresh SemVer for changed executable content, never edit an existing
publication. Inline source omits `artifact_digest`. Referenced artifacts require
their real canonical SHA-256 digest and source revision; the digest protects the
external artifact, not the entire Orchestrator definition.

Only one Proposed version may exist per Module. Prereleases cannot become
Default. Stable graduation publishes a separate stable Proposed Version and
deprecates the exact prerelease atomically. Default promotion must advance the
permitted lineage; restoration is restricted to the allowed predecessor.

Set `lifecycle_status` only if this configuration intentionally owns that
lifecycle. Omit it before delegating later promotion to another client; otherwise
Terraform may attempt to reconcile the configured status. Lifecycle transaction
resources are immutable receipts, not continuously enforced policies. Their
expected revisions are reviewed snapshots, not references to mutable resulting
`resource_version` attributes, which would otherwise cause repeated plans.

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
Archive does not deploy or destroy infrastructure.

Referenced Provider and Resource Type identities must also remain. Set
`deletion_policy = "retain"` explicitly on those dependencies to relinquish
Terraform ownership without modifying them. The default is `delete`, which
requests API deletion and reports reference conflicts. Retention does not imply
Provider archival or remove stored configuration.

Import retained resources before managing them again. Provider import IDs use
`provider-type.provider-id`; Resource Type and catalogue imports use their IDs.
Destroying immutable operation receipts never reverses the underlying commands.

## Exact Pins and notes

Pins protect exact effective Environment versions, including currently active
Deprecated versions. Pin UUIDs are importable into
`platform-orchestrator_module_version_pin`. Destroy performs an explicit audited
Unpin and requires the current scoped Core Unpin permission, not ownership by
the original creator and not an add-on override permission.

Bulk Pin/Unpin/Discard resources operate on explicit frozen Environment UUIDs,
not inherited selectors. Later Environments do not inherit a previous snapshot.
Append-only Pin notes do not change version, protection or activation boundary.
Deleting a note resource removes only its Terraform receipt, not the audit note.

## Partial failure and recovery

If publication succeeds but a requested promotion fails, the provider saves the
immutable Version identity to state before returning the error. Correct the
lifecycle input or permission and retry; do not recreate the Version under a new
identity to hide the failure. The same applies to catalogue creation followed by
failed archival. Investigate unexpected missing immutable Versions rather than
silently recreating history.
