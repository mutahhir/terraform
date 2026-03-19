runbook {
  terraform_version = ">= 1.0.0"

  provider "simple" {}
}

step "invoke_resource" {
  action "action_example" "target" {
    config {
      attr = test_resource.target.value
    }
  }

  execute {
    action_invoke {
      action = action.action_example.target
    }
  }

  output "invoked" {
    value = true
  }
}
