// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package e2etest

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	commandtesting "github.com/hashicorp/terraform/internal/command/testing"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/e2e"
	"github.com/hashicorp/terraform/internal/grpcwrap"
	tfplugin "github.com/hashicorp/terraform/internal/plugin6"
	"github.com/hashicorp/terraform/internal/providers"
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

	provider := commandtesting.NewProvider(nil)
	provider.Provider.PlanActionFn = func(req providers.PlanActionRequest) providers.PlanActionResponse {
		return providers.PlanActionResponse{}
	}
	provider.Provider.GetProviderSchemaResponse.Actions = map[string]providers.ActionSchema{
		"action_example": {
			ConfigSchema: &configschema.Block{
				Attributes: map[string]*configschema.Attribute{
					"attr": {Type: cty.String, Optional: true},
				},
			},
		},
	}

	reattachCh := make(chan *plugin.ReattachConfig)
	closeCh := make(chan struct{})
	provider6 := &providerServer{ProviderServer: grpcwrap.Provider6(provider.Provider)}

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

	stdout, stderr, err = tf.Run("runbook", "plan")
	if err != nil {
		t.Fatalf("unexpected runbook plan error: %s\nstderr:\n%s", err, stderr)
	}

	if !strings.Contains(stdout, "Runbook plan:") {
		t.Fatalf("missing runbook plan header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "step invoke_resource [ready]") {
		t.Fatalf("missing runbook step in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "actions: 1") {
		t.Fatalf("missing action count in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "will invoke: action.action_example.target") {
		t.Fatalf("missing planned action address in output:\n%s", stdout)
	}

	cancel()
	<-closeCh
}
