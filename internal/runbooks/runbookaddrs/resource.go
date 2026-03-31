// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import "github.com/hashicorp/terraform/internal/addrs"

// DataSource is the relative address of a data source declared inside the
// current step, optionally selecting a specific instance.
type DataSource struct {
	Type string
	Name string
	Key  addrs.InstanceKey
}

func (d DataSource) String() string {
	base := "data." + d.Type + "." + d.Name
	if d.Key == nil {
		return base
	}
	return base + d.Key.String()
}

// List is the relative address of a list block declared inside the current
// step, optionally selecting a specific instance.
type List struct {
	Type string
	Name string
	Key  addrs.InstanceKey
}

func (l List) String() string {
	base := "list." + l.Type + "." + l.Name
	if l.Key == nil {
		return base
	}
	return base + l.Key.String()
}
