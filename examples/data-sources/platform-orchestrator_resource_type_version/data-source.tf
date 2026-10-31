data "platform-orchestrator_resource_type_version" "storage" {
  resource_type_id = "storage"
  version          = 2
}

output "storage_interface_uuid" {
  value = data.platform-orchestrator_resource_type_version.storage.uuid
}
