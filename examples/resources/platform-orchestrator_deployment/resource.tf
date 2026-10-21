resource "platform-orchestrator_deployment" "main" {
  project_id = "my-project"
  env_id     = "development"
  mode       = "deploy"
  manifest = jsonencode({
    workloads = {}
    shared    = {}
  })
  wait_for = true

  # While another deployment of the environment runs, wait in its deployment
  # queue (the default) instead of failing with environment_busy, and do not
  # start if the deployment it waited for failed.
  when_busy       = "queue"
  halt_on_failure = true

  # The create and update timeouts cover the wait in the queue.
  timeouts {
    create = "60m"
    update = "60m"
  }
}
