# Project pipeline draft (RC)

> **RC feature:** This page describes Deployment Pipelines in the coordinated
> release candidate (Provider `2.5.0-rc.1`). Pipelines are not available in the
> current stable release from `main`. Use matching Pipeline RC components; Native
> execution requires explicit operator activation.

The reviewed YAML document is shared with the Console, API and `octl` pipeline commands. JSON may be loaded directly with `file()`. YAML is converted with `jsonencode(yamldecode(file(...)))`; comments and formatting are not persisted in Terraform state.

This resource manages drafts only. Publishing, retirement, and deployment-use policy are separate explicit operations. A slug change replaces the draft; name and definition changes update it. JSON key ordering and formatting do not cause drift.

Updates and deletes use the state's `resource_version`. A concurrent edit fails rather than overwriting someone else's change; review and refresh before retrying. A duplicate slug on create requires an explicit import, never silent adoption. Delete archives an unused draft and preserves historical executions. First retire all published versions and remove allowed/default policy references.
