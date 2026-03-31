// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookaddrs

import "github.com/hashicorp/terraform/internal/addrs"

// DataSource is the relative address of a data source declared inside the
// current step.
type DataSource struct {
	Type string
	Name string
}

func (d DataSource) String() string {
	return "data." + d.Type + "." + d.Name
}

// DataSourceInstance is the relative address of a concrete data source
// instance declared inside the current step.
type DataSourceInstance struct {
	DataSource DataSource
	Key        addrs.InstanceKey
}

func (d DataSourceInstance) String() string {
	if d.Key == nil {
		return d.DataSource.String()
	}
	return d.DataSource.String() + d.Key.String()
}

// List is the relative address of a list block declared inside the current
// step.
type List struct {
	Type string
	Name string
}

func (l List) String() string {
	return "list." + l.Type + "." + l.Name
}

// ListInstance is the relative address of a concrete list block instance
// declared inside the current step.
type ListInstance struct {
	List List
	Key  addrs.InstanceKey
}

func (l ListInstance) String() string {
	if l.Key == nil {
		return l.List.String()
	}
	return l.List.String() + l.Key.String()
}
