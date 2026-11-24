The reviewed YAML document is shared with the Console, API and `octl` pipeline commands. JSON may be loaded directly with `file()`. YAML is converted with `jsonencode(yamldecode(file(...)))`; comments and formatting are not persisted in Terraform state.

This resource manages drafts only. Publishing, retirement, and deployment-use policy are separate explicit operations. A slug change replaces the draft; name and definition changes update it. JSON key ordering and formatting do not cause drift.

Updates and deletes use the state's `resource_version`. A concurrent edit fails rather than overwriting someone else's change; review and refresh before retrying. A duplicate slug on create requires an explicit import, never silent adoption. Delete archives an unused draft and preserves historical executions. First retire all published versions and remove allowed/default policy references.

The live lifecycle test needs an isolated `PIPELINE_TEST_PROJECT` and the normal `PO_API_URL`, `PO_ORG_ID`, `PO_AUTH_TOKEN` variables:

```sh
TF_ACC=1 go test ./internal/provider -run '^TestAccPipelineResource$' -count=1
```
