// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package rpcapi

import (
	"context"
	"errors"
	"net/rpc"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/runbooks"
	"github.com/hashicorp/terraform/internal/rpcapi/terraform1/setup"
)

var Handshake = handshake

type GRPCCorePlugin struct {
	plugin.GRPCPlugin
}

func (p *GRPCCorePlugin) Server(*plugin.MuxBroker) (interface{}, error) {
	return nil, errors.New("rpcapi only implements gRPC clients")
}

func (p *GRPCCorePlugin) Client(*plugin.MuxBroker, *rpc.Client) (interface{}, error) {
	return nil, errors.New("rpcapi only implements gRPC clients")
}

func (p *GRPCCorePlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	_ = ctx
	_ = broker
	return &GRPCCoreClient{conn: c}, nil
}

func (p *GRPCCorePlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	return errors.ErrUnsupported
}

type GRPCCoreClient struct {
	conn *grpc.ClientConn
}

func (c *GRPCCoreClient) Setup() setup.SetupClient {
	return setup.NewSetupClient(c.conn)
}

func (c *GRPCCoreClient) Runbooks() runbooks.RunbooksClient {
	return runbooks.NewRunbooksClient(c.conn)
}
