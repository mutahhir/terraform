runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

variable "provider_value" {
  type        = string
  description = "Provider value used for testing"
  default     = "hello"
}

provider "simple" {}

step "invoke_resource" {
  precondition {
    condition     = workspace.output.enabled
    error_message = "workspace output must enable this step"
    on_fail       = "skip"
  }

  data "simple_resource" "current" {
    value = var.provider_value
  }

  list "simple_resource" "inventory" {
    provider = simple

    config {
      value = var.provider_value
    }

    include_resource = true
    limit            = 10
  }

  action "simple_action" "target" {
    config {
      value = data.simple_resource.current.value
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.target
    }
  }

  postcondition {
    condition     = strcontains(action.simple_action.target.output, "Hello world!")
    error_message = "action output must be available after execution"
  }

  output "invoked" {
    value = true
  }

  output "action_output" {
    value = action.simple_action.target.output
  }
}
