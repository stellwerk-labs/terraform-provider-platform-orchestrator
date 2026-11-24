resource "platform-orchestrator_pipeline" "release" {
  project_id = "my-project"
  document = jsonencode({
    slug       = "direct-apply"
    name       = "Direct apply"
    definition = { strategy = "apply", steps = [] }
  })
}

resource "platform-orchestrator_pipeline_version" "release" {
  project_id                = "my-project"
  pipeline_id               = platform-orchestrator_pipeline.release.id
  expected_resource_version = platform-orchestrator_pipeline.release.resource_version
  request_key               = "reviewed-direct-apply-revision-1"

  lifecycle {
    create_before_destroy = true
  }
}

# Move policy references before retiring an old version.
# Review the draft and change request_key when publishing a different revision.
