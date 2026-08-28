resource "platform-orchestrator_metadata_key" "cost_center" {
  name        = "cost-center"
  description = "Internal cost-center identifier"
  schema_type = "string"
  pattern     = "^[A-Z0-9-]+$"
}
