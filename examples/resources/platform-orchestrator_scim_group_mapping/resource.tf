resource "platform-orchestrator_scim_group_mapping" "platform_engineers" {
  group_display_name = "Platform Engineers"
  role_id            = platform-orchestrator_role.module_maintainer.id
}
