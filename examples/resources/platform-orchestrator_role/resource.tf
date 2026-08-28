data "platform-orchestrator_permissions" "available" {}

resource "platform-orchestrator_role" "module_maintainer" {
  display_name = "Module Maintainer"
  permissions = [
    "module_read",
    "module_write",
  ]
}
