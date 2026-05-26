package addrs

// Referenceable is implemented by all address types that can appear as the
// subject of a runbook reference (e.g., step.name, local.x, data.type.name).
//
// This is intentionally minimal (just String()) because runbook references
// also use Terraform core address types (Resource, Action, LocalValue) which
// live in internal/addrs. Adding methods beyond String() would require either:
// - Adding runbook-specific methods to core types (invasive)
// - Wrapper types around every core type (complex)
//
// The String() representation is unique within each address kind because it
// includes the full traversal path (e.g., "data.aws_ami.latest",
// "step.deploy.result").
type Referenceable interface {
	String() string
}
