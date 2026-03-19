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
	"github.com/hashicorp/terraform/internal/e2e"
	"github.com/hashicorp/terraform/internal/grpcwrap"
	tfplugin "github.com/hashicorp/terraform/internal/plugin6"
	simple "github.com/hashicorp/terraform/internal/provider-simple-v6"
	proto "github.com/hashicorp/terraform/internal/tfplugin6"
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
	if !strings.Contains(stdout, "queries: 1") {
		t.Fatalf("missing query count in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "will invoke: action.simple_action.target") {
		t.Fatalf("missing planned action address in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "will list: list.simple_resource.inventory") {
		t.Fatalf("missing planned query address in output:\n%s", stdout)
	}

	cancel()
	<-closeCh
}
