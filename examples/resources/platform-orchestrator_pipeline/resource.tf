resource "platform-orchestrator_pipeline" "reviewed" {
  project_id = "my-project"
  document   = jsonencode(yamldecode(file("${path.module}/reviewed.yaml")))
}

# JSON alternative: document = file("${path.module}/pipeline.json")
# Import explicitly with: terraform import platform-orchestrator_pipeline.reviewed my-project/PIPELINE_UUID
