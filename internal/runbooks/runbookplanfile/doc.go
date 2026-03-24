// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

// Package runbookplanfile implements the persisted .tfrunplan file format.
//
// A .tfrunplan file is a single-file container, modeled after Terraform's
// saved plan file format, that captures the runbook execution order and the
// per-step lowered artifacts needed for later execution.
package runbookplanfile
