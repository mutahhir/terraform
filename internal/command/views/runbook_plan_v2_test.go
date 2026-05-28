package views

import (
	"strings"
	"testing"

	"github.com/mitchellh/colorstring"
	"github.com/zclconf/go-cty/cty"
)

func testColorize() *colorstring.Colorize {
	return &colorstring.Colorize{
		Colors:  colorstring.DefaultColors,
		Disable: true,
		Reset:   true,
	}
}

func TestRenderActionAttributes(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"target":  cty.StringVal("srv-123"),
		"integer": cty.NumberIntVal(42),
		"enabled": cty.True,
	})

	var b strings.Builder
	sym := asciiPlanSymbols()
	renderActionAttributes(&b, v, 7, sym, testColorize())
	got := b.String()

	t.Logf("rendered:\n%s", got)

	// Keys should NOT be quoted
	if strings.Contains(got, `"target"`) {
		t.Errorf("keys should not be quoted, got: %s", got)
	}
	// Values should be properly formatted
	if !strings.Contains(got, `target  = "srv-123"`) {
		t.Errorf("expected aligned target attr, got: %s", got)
	}
	if !strings.Contains(got, `integer = 42`) {
		t.Errorf("expected integer attr, got: %s", got)
	}
	if !strings.Contains(got, `enabled = true`) {
		t.Errorf("expected enabled attr, got: %s", got)
	}
	// Should be indented by 7 spaces
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "       ") {
			t.Errorf("expected 7-space indent, got: %q", line)
		}
	}
}

func TestRenderActionAttributes_Unknown(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"id":   cty.UnknownVal(cty.String),
		"name": cty.StringVal("my-resource"),
	})

	var b strings.Builder
	sym := asciiPlanSymbols()
	renderActionAttributes(&b, v, 7, sym, testColorize())
	got := b.String()

	t.Logf("rendered:\n%s", got)

	if !strings.Contains(got, `id   = (known after apply)`) {
		t.Errorf("expected unknown value marker, got: %s", got)
	}
	if !strings.Contains(got, `name = "my-resource"`) {
		t.Errorf("expected name value, got: %s", got)
	}
}

func TestRenderActionAttributes_NilValue(t *testing.T) {
	var b strings.Builder
	sym := asciiPlanSymbols()
	renderActionAttributes(&b, cty.NilVal, 7, sym, testColorize())
	if b.Len() != 0 {
		t.Errorf("expected empty output for nil value, got: %s", b.String())
	}
}
