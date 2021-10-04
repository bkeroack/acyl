// Package client is an abstract API client for a Furan RPC service using API key authentication
package client

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/gofrs/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/dollarshaveclub/furan/v2/pkg/generated/furanrpc"
	fgrpc "github.com/dollarshaveclub/furan/v2/pkg/grpc"
)

type rpcCreds struct {
	apilabel, apikey string
}

func (c *rpcCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		c.apilabel: c.apikey,
	}, nil
}

func (c *rpcCreds) RequireTransportSecurity() bool {
	return true
}

var _ credentials.PerRPCCredentials = &rpcCreds{}

// Options contains options for the RPC client
type Options struct {
	Address               string // Furan RPC server address (host:port)
	APIKey                string // RPC API key
	TLSInsecureSkipVerify bool   // Skip verifying TLS certificates (INSECURE)
}

// New returns a Furan RPC client connected to Options.Address using Options.APIKey for authentication.
// TLS is enabled by default unless specifically disabled.
// You must call Close() on the client to close the connection and free resources when finished
func New(opts Options) (*RemoteBuilder, error) {
	rc := &rpcCreds{
		apilabel: fgrpc.APIKeyLabel,
		apikey:   opts.APIKey,
	}
	conn, err := grpc.Dial(
		opts.Address,
		grpc.WithPerRPCCredentials(rc),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: opts.TLSInsecureSkipVerify})),
	)
	if err != nil {
		return nil, fmt.Errorf("error connecting to grpc server: %w", err)
	}
	client := furanrpc.NewFuranExecutorClient(conn)
	return &RemoteBuilder{
		conn: conn,
		c:    client,
	}, nil
}

// RemoteBuilder is a Furan RPC client
type RemoteBuilder struct {
	conn *grpc.ClientConn
	c    furanrpc.FuranExecutorClient
}

// Close closes any active connections to the RPC server
func (rb *RemoteBuilder) Close() {
	rb.conn.Close()
}

// StartBuild starts a build and returns the id or error
func (rb *RemoteBuilder) StartBuild(ctx context.Context, req furanrpc.BuildRequest) (uuid.UUID, error) {
	resp, err := rb.c.StartBuild(ctx, &req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("error starting build: %w", err)
	}
	id, err := uuid.FromString(resp.BuildId)
	if err != nil {
		return uuid.Nil, fmt.Errorf("malformed build id: %w", err)
	}
	return id, nil
}

// GetBuildStatus gets the current build status for the build id
func (rb *RemoteBuilder) GetBuildStatus(ctx context.Context, id uuid.UUID) (*furanrpc.BuildStatusResponse, error) {
	resp, err := rb.c.GetBuildStatus(ctx, &furanrpc.BuildStatusRequest{
		BuildId: id.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("error getting build status: %w", err)
	}
	return resp, nil
}

func (rb *RemoteBuilder) GetBuildEvents(ctx context.Context, id uuid.UUID) (*furanrpc.BuildEventsResponse, error) {
	resp, err := rb.c.GetBuildEvents(ctx, &furanrpc.BuildStatusRequest{
		BuildId: id.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("error getting build events: %w", err)
	}
	return resp, nil
}

// CancelBuild requests cancellation for a currently running build
func (rb *RemoteBuilder) CancelBuild(ctx context.Context, id uuid.UUID) error {
	_, err := rb.c.CancelBuild(ctx, &furanrpc.BuildCancelRequest{
		BuildId: id.String(),
	})
	return err
}

// MonitorBuild opens a streaming RPC connection to monitor build events for a currently-running build
// Call the Recv() method on the returned monitor client to receive build messages
// Cancel the context when finished to close the connection
func (rb *RemoteBuilder) MonitorBuild(ctx context.Context, id uuid.UUID) (furanrpc.FuranExecutor_MonitorBuildClient, error) {
	return rb.c.MonitorBuild(ctx, &furanrpc.BuildStatusRequest{
		BuildId: id.String(),
	})
}

func (rb *RemoteBuilder) ListBuilds(ctx context.Context, req furanrpc.ListBuildsRequest) ([]furanrpc.BuildStatusResponse, error) {
	resp, err := rb.c.ListBuilds(ctx, &req)
	if err != nil {
		return nil, fmt.Errorf("error listing builds: %w", err)
	}
	out := make([]furanrpc.BuildStatusResponse, len(resp.Builds))
	for i := range resp.Builds {
		bs := resp.Builds[i]
		if bs != nil {
			out[i] = *bs
		}
	}
	return out, nil
}
