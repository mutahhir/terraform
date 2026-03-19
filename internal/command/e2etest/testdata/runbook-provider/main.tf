terraform {
  required_providers {
    simple = {
      source = "hashicorp/test"
    }
  }
}

resource "test_resource" "target" {
  value = "hello"
}
