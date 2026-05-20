package addrs

import (
	"strings"

	terraformaddrs "github.com/hashicorp/terraform/internal/addrs"
)

type WorkspaceModulePath struct {
	Calls []WorkspaceModuleCall
}

type WorkspaceModuleCall struct {
	Name        string
	InstanceKey terraformaddrs.InstanceKey
}

func (p WorkspaceModulePath) String() string {
	if len(p.Calls) == 0 {
		return "workspace"
	}
	var b strings.Builder
	b.WriteString("workspace")
	for _, call := range p.Calls {
		b.WriteString(".module.")
		b.WriteString(call.Name)
		if call.InstanceKey != nil && call.InstanceKey != terraformaddrs.NoKey {
			b.WriteString(call.InstanceKey.String())
		}
	}
	return b.String()
}

type WorkspaceAction struct {
	Module WorkspaceModulePath
	Action terraformaddrs.Action
}

func (a WorkspaceAction) String() string {
	prefix := a.Module.String()
	if prefix == "workspace" {
		return prefix + "." + a.Action.String()
	}
	return prefix + ".action." + a.Action.Type + "." + a.Action.Name
}

type WorkspaceResource struct {
	Module   WorkspaceModulePath
	Resource terraformaddrs.Resource
}

func (r WorkspaceResource) String() string {
	return r.Module.String() + "." + r.Resource.String()
}
