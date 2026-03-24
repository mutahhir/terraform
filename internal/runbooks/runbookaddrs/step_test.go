// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import (
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
)

func TestStepInstanceStringSingleton(t *testing.T) {
	addr := Step{Name: "discover_roles"}.Instance(addrs.NoKey)
	if got, want := addr.String(), "discover_roles"; got != want {
		t.Fatalf("wrong singleton address: got %q want %q", got, want)
	}
}

func TestStepInstanceStringCount(t *testing.T) {
	addr := Step{Name: "inspect_role"}.Instance(addrs.IntKey(2))
	if got, want := addr.String(), "inspect_role[2]"; got != want {
		t.Fatalf("wrong count address: got %q want %q", got, want)
	}
}

func TestStepInstanceStringForEach(t *testing.T) {
	addr := Step{Name: "inspect_role"}.Instance(addrs.StringKey("admin"))
	if got, want := addr.String(), `inspect_role["admin"]`; got != want {
		t.Fatalf("wrong for_each address: got %q want %q", got, want)
	}
}

func TestStepInstanceStringForEachEscapesLikeTerraform(t *testing.T) {
	addr := Step{Name: "inspect_role"}.Instance(addrs.StringKey(`a"b`))
	if got, want := addr.String(), `inspect_role["a\"b"]`; got != want {
		t.Fatalf("wrong escaped address: got %q want %q", got, want)
	}
}
