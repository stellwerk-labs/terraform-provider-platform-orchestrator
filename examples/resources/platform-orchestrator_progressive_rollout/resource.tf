# Enterprise: requires the Progressive Rollouts plugin
# (dev.stellwerk.progressive-rollouts) to be installed, enabled and licensed.
resource "platform-orchestrator_progressive_rollout" "payments" {
  idempotency_key = "payments-1-1-0"
  desired_state   = "ready"
  control_reason  = "Roll out payments 1.1.0"
  spec = jsonencode({
    name              = "payments 1.1.0"
    default_promotion = "onSuccess"
    changes = [{
      module_id      = "payments"
      from           = "1.0.0"
      target_version = "1.1.0"
      target_digest  = ""
      target_status  = "default"
    }]
    selector = { environment_types = ["development", "production"] }
    waves = [{
      id                = "canary"
      name              = "Canary"
      environment_ids   = ["6f1c2a9e-8b3d-4c5e-9f7a-1b2c3d4e5f60"]
      approval          = "environment"
      progression       = "manual"
      bake_time_seconds = 600
      retry = {
        max_attempts             = 3
        initial_delay_seconds    = 30
        maximum_delay_seconds    = 300
        maximum_duration_seconds = 3600
      }
      max_concurrency = { absolute = 1 }
    }]
  })
}
