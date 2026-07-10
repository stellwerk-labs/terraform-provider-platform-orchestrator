terraform {
  required_providers {
    platform-orchestrator = {

      source  = "stellwerk-labs/platform-orchestrator"
      version = "~> 2.0"
    }
  }
}

provider "platform-orchestrator" {
  org_id = "organization"
}
