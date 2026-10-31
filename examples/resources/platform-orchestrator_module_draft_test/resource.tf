resource "platform-orchestrator_module_draft_test" "reviewed" {
  project_id                = "example"
  env_id                    = "development"
  module_id                 = "storage"
  draft_id                  = "11111111-1111-4111-8111-111111111111"
  capture_key               = "storage-first-review"
  expected_resource_version = 1
  reason                    = "Capture the exact implementation reviewed before execution"
}

resource "platform-orchestrator_deployment" "test" {
  project_id = "example"
  env_id     = "development"
  draft_test_snapshots = {
    (platform-orchestrator_module_draft_test.reviewed.module_uuid) = platform-orchestrator_module_draft_test.reviewed.id
  }
  manifest = jsonencode({
    workloads = {}
    shared = {
      storage = { type = "storage" }
    }
  })
}
