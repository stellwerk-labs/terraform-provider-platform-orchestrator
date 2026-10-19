data "platform-orchestrator_plugin_installation" "progressive_rollouts" {
  plugin_id = "dev.stellwerk.progressive-rollouts"
}

# The organization is the provider's org_id. In an installation that serves all organizations, Core created the enablement already; this resource adopts it.
resource "platform-orchestrator_plugin_enablement" "progressive_rollouts" {
  installation_id = data.platform-orchestrator_plugin_installation.progressive_rollouts.installation_id
  reason          = "Platform team adopts progressive rollouts for every application team"
  timezone        = "Europe/Berlin"
}
