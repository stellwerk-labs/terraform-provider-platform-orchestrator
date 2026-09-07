terraform {
  required_providers {
    platform-orchestrator = {
      source = "registry.terraform.io/stellwerk-labs/platform-orchestrator"
    }
  }
}

variable "project_uuid" {
  type = string
}

variable "environment_uuid" {
  type = string
}

variable "module_uuid" {
  type = string
}

variable "version_uuid" {
  type = string
}

variable "removal_reason" {
  type = string
}

variable "note_enabled" {
  type    = bool
  default = false
}

provider "platform-orchestrator" {}

resource "platform-orchestrator_module_version_pin" "release" {
  project_uuid     = var.project_uuid
  environment_uuid = var.environment_uuid
  module_uuid      = var.module_uuid
  version_uuid     = var.version_uuid
  reason           = "Preserve the exact effective acceptance version"
  removal_reason   = var.removal_reason
}

resource "platform-orchestrator_module_version_pin_note" "release" {
  count  = var.note_enabled ? 1 : 0
  pin_id = platform-orchestrator_module_version_pin.release.id
  note   = "Reviewed operational context; protection remains unchanged"
}
