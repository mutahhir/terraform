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

step "bootstrap" {
  list "simple_resource" "inventory" {
    provider = simple

    config {
      value = var.provider_value
    }

    include_resource = true
    limit            = 1
  }

  data "simple_resource" "current" {
    value = var.provider_value
  }

  action "simple_action" "seed" {
    config {
      value = data.simple_resource.current.value
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

  output "count" {
    value = length(list.simple_resource.inventory.data)
  }

  output "seed_output" {
    value = action.simple_action.seed.output
  }
}

step "dependent" {
  precondition {
    condition     = steps.bootstrap.ready && steps.bootstrap.count >= 0
    error_message = "bootstrap must run first"
  }

  action "simple_action" "followup" {
    config {
      value = var.provider_value
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.followup
    }
  }

  output "done" {
    value = true
  }

  output "seen_seed_output" {
    value = steps.bootstrap.seed_output
  }
}

output "all_done" {
  value = steps.dependent.done
}

output "final_seed_output" {
  value = steps.dependent.seen_seed_output
}
