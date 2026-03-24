// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	version "github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform/internal/e2e"
	"github.com/hashicorp/terraform/internal/grpcwrap"
	tfplugin "github.com/hashicorp/terraform/internal/plugin6"
	simple "github.com/hashicorp/terraform/internal/provider-simple-v6"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/states/statefile"
	proto "github.com/hashicorp/terraform/internal/tfplugin6"
	"github.com/zclconf/go-cty/cty"
)

func TestRunbookPlan(t *testing.T) {
	if !canRunGoBuild {
		t.Skip("can't run without building a new provider executable")
	}

	t.Parallel()
	os.Setenv(e2e.TestExperimentFlag, "true")
	terraformBin := e2e.GoBuild("github.com/hashicorp/terraform", "terraform")

	fixturePath := filepath.Join("testdata", "runbook-provider")
	tf := e2e.NewBinary(t, terraformBin, fixturePath)

	reattachCh := make(chan *plugin.ReattachConfig)
	closeCh := make(chan struct{})
	provider6 := &providerServer{ProviderServer: grpcwrap.Provider6(simple.Provider())}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go plugin.Serve(&plugin.ServeConfig{
		Logger:     hclog.New(&hclog.LoggerOptions{Name: "plugintest", Level: hclog.Trace, Output: io.Discard}),
		Test:       &plugin.ServeTestConfig{Context: ctx, ReattachConfigCh: reattachCh, CloseCh: closeCh},
		GRPCServer: plugin.DefaultGRPCServer,
		VersionedPlugins: map[int]plugin.PluginSet{
			6: {
				"provider": &tfplugin.GRPCProviderPlugin{GRPCProvider: func() proto.ProviderServer { return provider6 }},
			},
		},
	})
	config := <-reattachCh
	reattachStr, err := json.Marshal(map[string]reattachConfig{
		"hashicorp/test": {
			Protocol:        string(config.Protocol),
			ProtocolVersion: 6,
			Pid:             config.Pid,
			Test:            true,
			Addr:            reattachConfigAddr{Network: config.Addr.Network(), String: config.Addr.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tf.AddEnv("TF_REATTACH_PROVIDERS=" + string(reattachStr))

	state := states.NewState()
	state.RootOutputValues["enabled"] = &states.OutputValue{Value: cty.True, Sensitive: false}
	stateFile := &statefile.File{
		Lineage:          "runbook-test",
		Serial:           1,
		TerraformVersion: version.Must(version.NewVersion("1.0.0")),
		State:            state,
	}
	var stateBuf bytes.Buffer
	if err := statefile.WriteForTest(stateFile, &stateBuf); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tf.Path("states", "default"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tf.Path("states", "default", "terraform.tfstate"), stateBuf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tf.Path("terraform.tfstate"), stateBuf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := tf.Run("init")
	if err != nil {
		t.Fatalf("unexpected init error: %s\nstderr:\n%s", err, stderr)
	}
	_ = stdout

	stdout, stderr, err = tf.Run("runbook", "init")
	if err != nil {
		t.Fatalf("unexpected runbook init error: %s\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "Runbook initialized.") {
		t.Fatalf("missing runbook init confirmation:\n%s", stdout)
	}
	if _, err := os.Stat(tf.Path(".terraform", "runbook-state.json")); err != nil {
		t.Fatalf("expected runbook state file to exist: %s", err)
	}

	stdout, stderr, err = tf.Run("runbook", "plan")
	if err != nil {
		t.Fatalf("unexpected runbook plan error: %s\nstderr:\n%s", err, stderr)
	}

	if !strings.Contains(stdout, "Runbook Execution Plan") {
		t.Fatalf("missing runbook plan header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# Step 1: invoke_resource") {
		t.Fatalf("missing runbook step in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "actions = [") || !strings.Contains(stdout, "action.simple_action.target") {
		t.Fatalf("missing planned action address in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "queries = [") || !strings.Contains(stdout, "list.simple_resource.inventory") {
		t.Fatalf("missing planned query address in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "data_reads = [") || !strings.Contains(stdout, "data.simple_resource.current") {
		t.Fatalf("missing planned data address in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Plan: 1 to execute, 0 to skip.") || !strings.Contains(stdout, "Order: invoke_resource") {
		t.Fatalf("missing runbook plan footer summary:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Saved the runbook plan to:") {
		t.Fatalf("missing saved runbook plan message:\n%s", stdout)
	}
	if _, err := os.Stat(tf.Path(".terraform", "runbook.tfrunplan")); err != nil {
		t.Fatalf("expected runbook plan file to exist: %s", err)
	}

	stdout, stderr, err = tf.Run("runbook", "execute")
	if err != nil {
		t.Fatalf("unexpected runbook execute error: %s\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "Runbook Apply") {
		t.Fatalf("missing runbook execute header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# invoke_resource") || !strings.Contains(stdout, "status = \"complete\"") {
		t.Fatalf("missing runbook execute step completion:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Runbook apply complete.") {
		t.Fatalf("missing runbook execute completion:\n%s", stdout)
	}

	cancel()
	<-closeCh
}

func TestRunbookExecuteMultiStep(t *testing.T) {
	if !canRunGoBuild {
		t.Skip("can't run without building a new provider executable")
	}

	t.Parallel()
	os.Setenv(e2e.TestExperimentFlag, "true")
	terraformBin := e2e.GoBuild("github.com/hashicorp/terraform", "terraform")

	fixturePath := filepath.Join("testdata", "runbook-provider-multistep")
	tf := e2e.NewBinary(t, terraformBin, fixturePath)

	reattachCh := make(chan *plugin.ReattachConfig)
	closeCh := make(chan struct{})
	provider6 := &providerServer{ProviderServer: grpcwrap.Provider6(simple.Provider())}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go plugin.Serve(&plugin.ServeConfig{
		Logger:     hclog.New(&hclog.LoggerOptions{Name: "plugintest", Level: hclog.Trace, Output: io.Discard}),
		Test:       &plugin.ServeTestConfig{Context: ctx, ReattachConfigCh: reattachCh, CloseCh: closeCh},
		GRPCServer: plugin.DefaultGRPCServer,
		VersionedPlugins: map[int]plugin.PluginSet{
			6: {
				"provider": &tfplugin.GRPCProviderPlugin{GRPCProvider: func() proto.ProviderServer { return provider6 }},
			},
		},
	})
	config := <-reattachCh
	reattachStr, err := json.Marshal(map[string]reattachConfig{
		"hashicorp/test": {
			Protocol:        string(config.Protocol),
			ProtocolVersion: 6,
			Pid:             config.Pid,
			Test:            true,
			Addr:            reattachConfigAddr{Network: config.Addr.Network(), String: config.Addr.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tf.AddEnv("TF_REATTACH_PROVIDERS=" + string(reattachStr))

	stdout, stderr, err := tf.Run("init")
	if err != nil {
		t.Fatalf("unexpected init error: %s\nstderr:\n%s", err, stderr)
	}
	_ = stdout

	stdout, stderr, err = tf.Run("runbook", "init")
	if err != nil {
		t.Fatalf("unexpected runbook init error: %s\nstderr:\n%s", err, stderr)
	}
	stdout, stderr, err = tf.Run("runbook", "plan")
	if err != nil {
		t.Fatalf("unexpected runbook plan error: %s\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "# Step 1: bootstrap") || !strings.Contains(stdout, "# Step 2: dependent") {
		t.Fatalf("missing multi-step plan output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Order: bootstrap -> dependent") {
		t.Fatalf("missing dependency order in plan output:\n%s", stdout)
	}

	stdout, stderr, err = tf.Run("runbook", "execute")
	if err != nil {
		t.Fatalf("unexpected runbook execute error: %s\nstderr:\n%s", err, stderr)
	}
	bootstrapIdx := strings.Index(stdout, "# bootstrap")
	dependentIdx := strings.Index(stdout, "# dependent")
	if bootstrapIdx == -1 || dependentIdx == -1 {
		t.Fatalf("missing execute completion lines:\n%s", stdout)
	}
	if bootstrapIdx > dependentIdx {
		t.Fatalf("dependent step completed before bootstrap:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Runbook apply complete.") {
		t.Fatalf("missing execute completion footer:\n%s", stdout)
	}

	cancel()
	<-closeCh
}

func TestRunbookPlanForEachStepExpansion(t *testing.T) {
	if !canRunGoBuild {
		t.Skip("can't run without building a new provider executable")
	}

	t.Parallel()
	os.Setenv(e2e.TestExperimentFlag, "true")
	terraformBin := e2e.GoBuild("github.com/hashicorp/terraform", "terraform")

	fixturePath := filepath.Join("testdata", "runbook-provider-foreach")
	tf := e2e.NewBinary(t, terraformBin, fixturePath)

	reattachCh := make(chan *plugin.ReattachConfig)
	closeCh := make(chan struct{})
	provider6 := &providerServer{ProviderServer: grpcwrap.Provider6(simple.Provider())}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go plugin.Serve(&plugin.ServeConfig{
		Logger:     hclog.New(&hclog.LoggerOptions{Name: "plugintest", Level: hclog.Trace, Output: io.Discard}),
		Test:       &plugin.ServeTestConfig{Context: ctx, ReattachConfigCh: reattachCh, CloseCh: closeCh},
		GRPCServer: plugin.DefaultGRPCServer,
		VersionedPlugins: map[int]plugin.PluginSet{
			6: {
				"provider": &tfplugin.GRPCProviderPlugin{GRPCProvider: func() proto.ProviderServer { return provider6 }},
			},
		},
	})
	config := <-reattachCh
	reattachStr, err := json.Marshal(map[string]reattachConfig{
		"hashicorp/test": {
			Protocol:        string(config.Protocol),
			ProtocolVersion: 6,
			Pid:             config.Pid,
			Test:            true,
			Addr:            reattachConfigAddr{Network: config.Addr.Network(), String: config.Addr.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tf.AddEnv("TF_REATTACH_PROVIDERS=" + string(reattachStr))

	if _, stderr, err := tf.Run("init"); err != nil {
		t.Fatalf("unexpected init error: %s\nstderr:\n%s", err, stderr)
	}
	if _, stderr, err := tf.Run("runbook", "init"); err != nil {
		t.Fatalf("unexpected runbook init error: %s\nstderr:\n%s", err, stderr)
	}
	stdout, stderr, err := tf.Run("runbook", "plan")
	if err != nil {
		t.Fatalf("unexpected runbook plan error: %s\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "# Step 1: discover_roles") {
		t.Fatalf("missing discover_roles plan output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# Step 2: inspect_role[") {
		t.Fatalf("missing expanded inspect_role instance output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "expanded_from = \"inspect_role\"") {
		t.Fatalf("missing expanded step metadata:\n%s", stdout)
	}

	cancel()
	<-closeCh
}

func TestRunbookExecuteForEachStepExpansion(t *testing.T) {
	if !canRunGoBuild {
		t.Skip("can't run without building a new provider executable")
	}

	t.Parallel()
	os.Setenv(e2e.TestExperimentFlag, "true")
	terraformBin := e2e.GoBuild("github.com/hashicorp/terraform", "terraform")

	fixturePath := filepath.Join("testdata", "runbook-provider-foreach")
	tf := e2e.NewBinary(t, terraformBin, fixturePath)

	reattachCh := make(chan *plugin.ReattachConfig)
	closeCh := make(chan struct{})
	provider6 := &providerServer{ProviderServer: grpcwrap.Provider6(simple.Provider())}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go plugin.Serve(&plugin.ServeConfig{
		Logger:     hclog.New(&hclog.LoggerOptions{Name: "plugintest", Level: hclog.Trace, Output: io.Discard}),
		Test:       &plugin.ServeTestConfig{Context: ctx, ReattachConfigCh: reattachCh, CloseCh: closeCh},
		GRPCServer: plugin.DefaultGRPCServer,
		VersionedPlugins: map[int]plugin.PluginSet{
			6: {
				"provider": &tfplugin.GRPCProviderPlugin{GRPCProvider: func() proto.ProviderServer { return provider6 }},
			},
		},
	})
	config := <-reattachCh
	reattachStr, err := json.Marshal(map[string]reattachConfig{
		"hashicorp/test": {
			Protocol:        string(config.Protocol),
			ProtocolVersion: 6,
			Pid:             config.Pid,
			Test:            true,
			Addr:            reattachConfigAddr{Network: config.Addr.Network(), String: config.Addr.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tf.AddEnv("TF_REATTACH_PROVIDERS=" + string(reattachStr))

	if _, stderr, err := tf.Run("init"); err != nil {
		t.Fatalf("unexpected init error: %s\nstderr:\n%s", err, stderr)
	}
	if _, stderr, err := tf.Run("runbook", "init"); err != nil {
		t.Fatalf("unexpected runbook init error: %s\nstderr:\n%s", err, stderr)
	}
	if _, stderr, err := tf.Run("runbook", "plan"); err != nil {
		t.Fatalf("unexpected runbook plan error: %s\nstderr:\n%s", err, stderr)
	}
	stdout, stderr, err := tf.Run("runbook", "execute")
	if err != nil {
		t.Fatalf("unexpected runbook execute error: %s\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "# inspect_role[") {
		t.Fatalf("missing expanded inspect_role execute output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "role = \"object-with-3-attributes\"") {
		t.Fatalf("missing instance output value:\n%s", stdout)
	}

	cancel()
	<-closeCh
}
