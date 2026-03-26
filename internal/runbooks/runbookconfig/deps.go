// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookconfig

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/terraform/internal/tfdiags"
)

type StepDependencies struct {
	Dependencies map[string][]string
}

func AnalyzeDependencies(cfg *Config) (StepDependencies, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := StepDependencies{Dependencies: make(map[string][]string)}
	if cfg == nil {
		return ret, diags
	}

	steps := make(map[string]*Step)
	for _, file := range cfg.Files {
		for name, step := range file.Steps {
			steps[name] = step
		}
	}

	for name, step := range steps {
		deps, moreDiags := dependenciesForStep(step, steps)
		diags = diags.Append(moreDiags)
		ret.Dependencies[name] = deps
	}

	for name := range steps {
		visited := map[string]bool{}
		stack := map[string]bool{}
		if cycle := detectDependencyCycle(name, ret.Dependencies, visited, stack); cycle != "" {
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Cyclic step dependency",
				fmt.Sprintf("Step %q participates in a dependency cycle involving %q.", name, cycle),
			))
		}
	}

	return ret, diags
}

func RunnableSteps(cfg *Config, completed map[string]bool) ([]string, tfdiags.Diagnostics) {
	deps, diags := AnalyzeDependencies(cfg)
	if diags.HasErrors() {
		return nil, diags
	}

	var runnable []string
	for step, stepDeps := range deps.Dependencies {
		if completed[step] {
			continue
		}
		ready := true
		for _, dep := range stepDeps {
			if !completed[dep] {
				ready = false
				break
			}
		}
		if ready {
			runnable = append(runnable, step)
		}
	}
	sort.Strings(runnable)
	return runnable, diags
}

func dependenciesForStep(step *Step, allSteps map[string]*Step) ([]string, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if step == nil {
		return nil, diags
	}

	depsSet := map[string]struct{}{}
	collect := func(expr hcl.Expression) {
		for _, dep := range stepOutputDependencies(expr) {
			if dep == step.Name {
				continue
			}
			if _, exists := allSteps[dep]; !exists {
				diags = diags.Append(tfdiags.Sourceless(
					tfdiags.Error,
					"Unknown step reference",
					fmt.Sprintf("Step %q references outputs from undeclared step %q.", step.Name, dep),
				))
				continue
			}
			depsSet[dep] = struct{}{}
		}
	}

	for _, condition := range step.Preconditions {
		if condition != nil {
			collect(condition.Condition)
			collect(condition.ErrorMessage)
		}
	}
	for _, condition := range step.Postconditions {
		if condition != nil {
			collect(condition.Condition)
			collect(condition.ErrorMessage)
		}
	}
	for _, expr := range step.Locals {
		collect(expr)
	}
	for _, output := range step.Outputs {
		if output != nil {
			collect(output.Value)
		}
	}

	deps := make([]string, 0, len(depsSet))
	for dep := range depsSet {
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	return deps, diags
}

func stepOutputDependencies(expr hcl.Expression) []string {
	if expr == nil {
		return nil
	}
	var deps []string
	for _, traversal := range expr.Variables() {
		if len(traversal) < 2 {
			continue
		}
		root, ok := traversal[0].(hcl.TraverseRoot)
		if !ok || root.Name != "steps" {
			continue
		}
		stepAttr, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		if len(traversal) == 2 {
			deps = append(deps, stepAttr.Name)
			continue
		}
		deps = append(deps, stepAttr.Name)
	}
	return deps
}

func detectDependencyCycle(current string, deps map[string][]string, visited, stack map[string]bool) string {
	if stack[current] {
		return current
	}
	if visited[current] {
		return ""
	}
	visited[current] = true
	stack[current] = true
	for _, dep := range deps[current] {
		if cycle := detectDependencyCycle(dep, deps, visited, stack); cycle != "" {
			return cycle
		}
	}
	delete(stack, current)
	return ""
}
