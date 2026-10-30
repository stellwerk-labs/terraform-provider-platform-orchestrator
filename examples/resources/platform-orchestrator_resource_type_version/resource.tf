resource "platform-orchestrator_resource_type" "storage" {
  id            = "storage"
  output_schema = jsonencode({ type = "object" })
}

resource "platform-orchestrator_resource_type_version" "storage_v2" {
  resource_type_id          = platform-orchestrator_resource_type.storage.id
  version                   = 2
  expected_resource_version = 1 # The reviewed catalogue revision, not a live value that changes after publication.
  reason                    = "Add an endpoint to the storage interface"
  output_schema = jsonencode({
    type       = "object"
    properties = { endpoint = { type = "string" } }
  })
}
