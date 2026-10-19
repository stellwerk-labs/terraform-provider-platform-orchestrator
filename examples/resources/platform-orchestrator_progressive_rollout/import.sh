# Import by the Rollout UUID. The Rollout does not return idempotency_key or
# control_reason, so write them in the configuration (generated configuration
# leaves them null); the first apply stores them without a plugin call.
terraform import platform-orchestrator_progressive_rollout.payments "6f1c2a9e-8b3d-4c5e-9f7a-1b2c3d4e5f61"
