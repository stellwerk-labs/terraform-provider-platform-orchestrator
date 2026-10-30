data "platform-orchestrator_resource_type_versions" "storage" {
  resource_type_id = "storage"
}

output "storage_interface_history" {
  value = jsondecode(data.platform-orchestrator_resource_type_versions.storage.versions_json)
}
