data "platform-orchestrator_native_agent_runner" "example" {
  id = "example-native-agent"
}

output "runner_registration_uuid" {
  value = data.platform-orchestrator_native_agent_runner.example.uuid
}
