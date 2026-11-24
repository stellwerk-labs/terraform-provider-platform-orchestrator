# Supply the original caller-owned publication intent, not an invented revision/key.
# Core verifies project, version and authoring pipeline; GET cannot recover the key/revision.
terraform import platform-orchestrator_pipeline_version.release '{"project_id":"my-project","id":"PUBLISHED_VERSION_UUID","pipeline_id":"DRAFT_UUID","expected_resource_version":1,"request_key":"reviewed-direct-apply-revision-1"}'
