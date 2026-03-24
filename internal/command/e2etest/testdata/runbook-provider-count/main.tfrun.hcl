runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

variable "provider_value" {
  type    = string
  default = "hello"
}

provider "simple" {}

step "seed" {
  action "simple_action" "seed" {
    config {
      value = var.provider_value
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.seed
    }
  }

  output "ready" {
    value = true
  }
}

step "inspect_count" {
  count = 2

  precondition {
    condition     = steps.seed.ready
    error_message = "seed must run first"
  }

  action "simple_action" "inspect" {
    config {
      value = tostring(count.index)
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.inspect
    }
  }

  output "idx" {
    value = count.index
  }

  output "label" {
    value = tostring(count.index)
  }
}
