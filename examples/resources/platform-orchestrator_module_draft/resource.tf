resource "platform-orchestrator_module_draft" "storage" {
  module_id        = "storage"
  authoring_key    = "storage-1.2.0-authoring"
  semantic_version = "1.2.0"
  reason           = "Prepare the next storage implementation"
  status           = "open"

  definition = jsonencode({
    module_source      = "inline"
    module_source_code = "output \"name\" { value = \"first attempt\" }"
    module_inputs      = {}
    module_params      = {}
    provider_mapping   = {}
    dependencies       = {}
    coprovisioned      = []
    output_schema = {
      type       = "object"
      properties = { name = { type = "string" } }
    }
  })
}

# Apply edits while open. After reviewing the stored revision, change only
# status to "published" and reason to your publication reason, then apply again.
# This creates Proposed. Promotion to Default is a separate lifecycle action.
# Use a new authoring_key when starting another session after this one closes.
