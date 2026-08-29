terraform {
  required_providers {
    platform-orchestrator = {

      source  = "stellwerk-labs/platform-orchestrator"
      version = "~> 1.1"
    }
  }
}

provider "platform-orchestrator" {
  org_id = "organization"
}
