# Native Agent registration trial

Use a new, unassigned logical site's public Agent key and public `rollout-catalogue` export. The file is an envelope containing `supported_protocols`, `capabilities`, and the inner `native_execution` DTO. The example decodes it with `local.catalogue = jsondecode(file(var.approved_inventory_file))`, copies both declaration sets and sends only `jsonencode(local.catalogue.native_execution)` as inventory. Do not pass the entire envelope as `native_execution`. For a public YAML envelope, change the local expression to `yamldecode(file(var.approved_inventory_file))`; the inner DTO still uses `jsonencode(local.catalogue.native_execution)`. The opaque tools digest comes from the real exporter, never this provider. Bundled tooling does not imply connectivity.

Native v2 Runner IDs follow Core's `^[a-z][a-z0-9_-]+$` pattern with an explicit 2–128 character runtime limit, including underscores and trailing/repeated hyphens. This coordinated Native-only limit is narrower than the historical generic SDK's unbounded ID schema; it does not change legacy Runner IDs.

1. Export the public catalogue offline before initialization.
2. Apply the registration and read its server-issued `uuid` output.
3. Copy that UUID into the site's private configuration, outside Terraform.
4. Perform separately qualified explicit local storage initialization. Registration itself does not initialize storage, activate execution or assign Environments.

To disable an existing registration, explicitly set `native_execution = null` and apply. Key, capacity and UUID remain. A later reviewed non-null inventory change re-enables admission. An unchanged null inventory has no diff. A new resource cannot start with null inventory; computed values may resolve during apply.

A description-only update omits unchanged inventory. If Core was independently disabled after the plan was captured, Apply returns refresh/replan attention with state and UUID retained; the description subset may already have committed. Refresh actual state and review the next plan before re-enabling. JSON formatting alone does not replace inventory.

Native registrations and history are permanently retained in the first release. Destroy is refused by Core and retains Terraform state even without `prevent_destroy`; this sample uses that extra user policy too. Renaming the slug cannot delete the old registration. Do not use force-delete, state removal or old SQLite identity adoption as decommissioning. Use `description = ""` to clear displayed text, since Core cannot clear an existing description to null.

This resource is an unpublished local-candidate consumer of the signed Core contract. Actual Core/IAM/site qualification and coordinated public publication are separate gates. Never pass a private key, DSN, command-signing key or private local runtime configuration into these public fields.
