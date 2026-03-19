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
      value = var.provider_value
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.target
    }
  }

  output "invoked" {
    value = true
  }
}
