// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

// Package runbookconfig deals with decoding and some static validation of the
// Terraform Runbook language, which currently uses files with the suffix
// .tfrun.hcl.
//
// The Runbook language is intentionally separate from both the main Terraform
// module language and the stacks language so it can evolve with its own
// execution model while still reusing Terraform runtime capabilities later.
package runbookconfig
