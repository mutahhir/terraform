// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/hashicorp/go-plugin"

	"github.com/hashicorp/terraform/internal/rpcapi"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/dependencies"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/setup"
)

type RunbookCommand struct {
	Meta
}

func (c *RunbookCommand) Help() string {
	return strings.TrimSpace(`
Usage: terraform [global options] runbook plan

  Plans a runbook and prints the runnable steps and invoked actions.
`)
}

func (c *RunbookCommand) Synopsis() string {
	return "Plan Terraform runbooks"
}

func (c *RunbookCommand) Run(args []string) int {
	args = c.Meta.process(args)
	if len(args) != 1 || args[0] != "plan" {
		c.Ui.Error(c.Help())
		return 1
	}

	core, client, err := c.rpcCoreClient(context.Background())
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to start Terraform RPC client: %s", err))
		return 1
	}
	defer client.Kill()

	if _, err := core.Setup().Handshake(context.Background(), &setup.Handshake_Request{}); err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to complete RPC handshake: %s", err))
		return 1
	}
	ctx := context.Background()

	configPath := c.WorkingDir.RootModuleDir()
	openResp, err := core.Runbooks().OpenRunbookConfiguration(ctx, &runbooks.OpenRunbookConfiguration_Request{ConfigPath: configPath})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook configuration: %s", err))
		return 1
	}
	if len(openResp.Diagnostics) > 0 {
		for _, diag := range openResp.Diagnostics {
			c.Ui.Error(diag.Summary + ": " + diag.Detail)
		}
		if openResp.RunbookConfigHandle == 0 {
			return 1
		}
	}

	runbookRuntimeHandle, cleanupRuntime, err := c.openRunbookRuntime(ctx, core)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to open runbook runtime: %s", err))
		return 1
	}
	defer cleanupRuntime()

	stepsResp, err := core.Runbooks().FindRunbookConfigurationSteps(ctx, &runbooks.FindRunbookConfigurationSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to inspect runbook steps: %s", err))
		return 1
	}

	runnableResp, err := core.Runbooks().GetRunnableRunbookSteps(ctx, &runbooks.GetRunnableRunbookSteps_Request{
		RunbookConfigHandle: openResp.RunbookConfigHandle,
	})
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Failed to determine runnable steps: %s", err))
		return 1
	}
	for _, diag := range runnableResp.Diagnostics {
		c.Ui.Error(diag.Summary + ": " + diag.Detail)
	}

	stepNames := make([]string, 0, len(stepsResp.Config.Steps))
	for name := range stepsResp.Config.Steps {
		stepNames = append(stepNames, name)
	}
	sort.Strings(stepNames)

	c.Ui.Output("Runbook plan:")
	for _, stepName := range stepNames {
		step := stepsResp.Config.Steps[stepName]
		status := "blocked"
		for _, runnable := range runnableResp.StepNames {
			if runnable == stepName {
				status = "ready"
				break
			}
		}
		c.Ui.Output(fmt.Sprintf("- step %s [%s]", stepName, status))
		planResp, _ := core.Runbooks().PlanRunbookStep(ctx, &runbooks.PlanRunbookStep_Request{
			RunbookConfigHandle:  openResp.RunbookConfigHandle,
			RunbookRuntimeHandle: runbookRuntimeHandle,
			StepName:             stepName,
			Scope:                &runbooks.EvalScope{},
		})
		for _, diag := range planResp.Diagnostics {
			c.Ui.Output(fmt.Sprintf("  diagnostic: %s: %s", diag.Summary, diag.Detail))
		}
		for _, action := range planResp.PlannedActions {
			c.Ui.Output(fmt.Sprintf("  will invoke: %s", action.Address))
		}
		for _, query := range planResp.PlannedQueries {
			c.Ui.Output(fmt.Sprintf("  will list: %s", query.Address))
		}
		for _, file := range planResp.LoweredFiles {
			c.Ui.Output(fmt.Sprintf("  lowered file: %s", file.Path))
		}
		if step.ActionCount > 0 {
			c.Ui.Output(fmt.Sprintf("  actions: %d", step.ActionCount))
		}
		if step.ListCount > 0 {
			c.Ui.Output(fmt.Sprintf("  queries: %d", step.ListCount))
		}
		if step.ExecuteCount > 0 {
			c.Ui.Output(fmt.Sprintf("  execute blocks: %d", step.ExecuteCount))
		}
		if len(step.Outputs) > 0 {
			c.Ui.Output(fmt.Sprintf("  outputs: %s", strings.Join(step.Outputs, ", ")))
		}
	}

	return 0
}

func (c *RunbookCommand) openRunbookRuntime(ctx context.Context, core *rpcapi.GRPCCoreClient) (int64, func(), error) {
	deps := core.Dependencies()
	cleanup := func() {}

	locks, diags := c.lockedDependencies()
	if diags.HasErrors() {
		return 0, cleanup, fmt.Errorf(diags.Err().Error())
	}
	providerSelections := make([]*terraform1.ProviderPackage, 0, len(locks.AllProviders()))
	for _, lock := range locks.AllProviders() {
		hashes := lock.AllHashes()
		hashStrs := make([]string, len(hashes))
		for i, hash := range hashes {
			hashStrs[i] = hash.String()
		}
		providerSelections = append(providerSelections, &terraform1.ProviderPackage{
			SourceAddr: lock.Provider().String(),
			Version:    lock.Version().String(),
			Hashes:     hashStrs,
		})
	}

	lockResp, err := deps.CreateDependencyLocks(ctx, &dependencies.CreateDependencyLocks_Request{
		ProviderSelections: providerSelections,
	})
	if err != nil {
		return 0, cleanup, err
	}

	cacheResp, err := deps.OpenProviderPluginCache(ctx, &dependencies.OpenProviderPluginCache_Request{CacheDir: c.providerLocalCacheDir().BasePath()})
	if err != nil {
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
		return 0, cleanup, err
	}

	runtimeResp, err := core.Runbooks().OpenRunbookRuntime(ctx, &runbooks.OpenRunbookRuntime_Request{
		DependencyLocksHandle: lockResp.DependencyLocksHandle,
		ProviderCacheHandle:   cacheResp.ProviderCacheHandle,
	})
	if err != nil {
		if cacheResp.ProviderCacheHandle != 0 {
			_, _ = deps.CloseProviderPluginCache(ctx, &dependencies.CloseProviderPluginCache_Request{ProviderCacheHandle: cacheResp.ProviderCacheHandle})
		}
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
		return 0, cleanup, err
	}

	cleanup = func() {
		if runtimeResp.RunbookRuntimeHandle != 0 {
			_, _ = core.Runbooks().CloseRunbookRuntime(ctx, &runbooks.CloseRunbookRuntime_Request{RunbookRuntimeHandle: runtimeResp.RunbookRuntimeHandle})
		}
		if cacheResp.ProviderCacheHandle != 0 {
			_, _ = deps.CloseProviderPluginCache(ctx, &dependencies.CloseProviderPluginCache_Request{ProviderCacheHandle: cacheResp.ProviderCacheHandle})
		}
		if lockResp.DependencyLocksHandle != 0 {
			_, _ = deps.CloseDependencyLocks(ctx, &dependencies.CloseDependencyLocks_Request{DependencyLocksHandle: lockResp.DependencyLocksHandle})
		}
	}

	return runtimeResp.RunbookRuntimeHandle, cleanup, nil
}

func (c *RunbookCommand) rpcCoreClient(ctx context.Context) (*rpcapi.GRPCCoreClient, *plugin.Client, error) {
	terraformBin, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  rpcapi.Handshake,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Cmd:              exec.Command(terraformBin, "rpcapi"),
		VersionedPlugins: map[int]plugin.PluginSet{1: {"tfcore": &rpcapi.GRPCCorePlugin{}}},
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, err
	}

	raw, err := rpcClient.Dispense("tfcore")
	if err != nil {
		client.Kill()
		return nil, nil, err
	}

	core, ok := raw.(*rpcapi.GRPCCoreClient)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("unexpected rpc client type %T", raw)
	}

	_ = ctx
	return core, client, nil
}
