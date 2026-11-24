resource "platform-orchestrator_pipeline_policy" "release" {
  project_id = "my-project"
  document = jsonencode({
    enabled             = true
    allowed_version_ids = ["11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"]
    default_version_id  = "11111111-1111-4111-8111-111111111111"
    rollback_defaults = {
      default_version_id = "22222222-2222-4222-8222-222222222222"
    }
  })
}

# Replace the sample UUIDs with allowed published versions, for example
# platform-orchestrator_pipeline_version.release.id. Both directions are explicit.
# To remove this resource: first apply enabled=false, allowed_version_ids=[],
# no default_version_id/environment_default_version_ids, and rollback_defaults={}.
# Then remove it in a separate apply. Destroy never disables server protection.
