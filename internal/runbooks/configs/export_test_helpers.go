package runbookconfigs

import "github.com/hashicorp/hcl/v2"

// DecodeExecutionBlockForTest is an exported wrapper around decodeExecutionBlock
// for use in tests outside this package.
func DecodeExecutionBlockForTest(block *hcl.Block) (*Execution, hcl.Diagnostics) {
	return decodeExecutionBlock(block)
}
