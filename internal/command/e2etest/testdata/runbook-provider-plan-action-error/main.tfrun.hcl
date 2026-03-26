runbook {
  terraform_version = ">= 1.0.0"

  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

provider "simple" {}

step "invoke" {
  action "simple_action" "broken" {
    config {
      value = "hello"
    }
  }

  execute {
    action_invoke {
      action = action.simple_action.broken
    }
  }
}
