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

step "discover_roles" {
  list "simple_resource" "roles" {
    provider = simple

    config {
      value = var.provider_value
    }

    include_resource = true
    limit            = 2
  }

  execute {
    action_invoke {
      action = action.simple_action.seed
    }
  }

  action "simple_action" "seed" {
    config {
      value = var.provider_value
    }
  }

  output "roles" {
    value = [for role in list.simple_resource.roles.data : role.identity.id]
  }
}

step "inspect_role" {
  for_each = steps.discover_roles.roles

  precondition {
    condition     = length(steps.discover_roles.roles) > 0
    error_message = "discover_roles must return at least one role"
  }

  action "simple_action" "inspect" {
    config {
      value = each.value
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.inspect
    }
  }

  output "role" {
    value = each.value
  }

  output "ready" {
    value = true
  }
}
