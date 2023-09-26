/*
Copyright 2023 Avi Zimmerman <avi.zimmerman@gmail.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package passthrough provides a passthrough storage provider. This is intended
// to be used by nodes that don't host their own storage, but need to query the
// storage of other nodes.
package passthrough

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	v1 "github.com/webmeshproj/api/v1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/webmeshproj/webmesh/pkg/logging"
	"github.com/webmeshproj/webmesh/pkg/meshnet/transport"
	"github.com/webmeshproj/webmesh/pkg/storage"
)

// Ensure we satisfy the provider interface.
var _ storage.Provider = &Provider{}
var _ storage.Consensus = &Consensus{}
var _ storage.MeshStorage = &Storage{}

// Options are the passthrough options.
type Options struct {
	// Dialer is the dialer to use for connecting to other nodes.
	Dialer transport.NodeDialer
	// LogLevel is the log level to use.
	LogLevel string
}

// Provider is a storage provider that passes through all storage operations to another node
// in the cluster.
type Provider struct {
	Options
	log        *slog.Logger
	subCancels []func()
	closec     chan struct{}
	mu         sync.Mutex
}

// NewProvider returns a new passthrough storage provider.
func NewProvider(opts Options) *Provider {
	return &Provider{
		Options: opts,
		log:     logging.NewLogger(opts.LogLevel).With("component", "passthrough-storage"),
		closec:  make(chan struct{}),
	}
}

func (p *Provider) MeshStorage() storage.MeshStorage {
	return &Storage{p}
}

func (p *Provider) Consensus() storage.Consensus {
	return &Consensus{p}
}

func (p *Provider) Start(ctx context.Context) error {
	// No-op
	return nil
}

func (p *Provider) ListenPort() uint16 {
	return 0
}

func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.closec:
		return nil
	default:
		defer close(p.closec)
		for _, cancel := range p.subCancels {
			cancel()
		}
	}
	return nil
}

func (p *Provider) Bootstrap(ctx context.Context) error {
	return storage.ErrNotStorageNode
}

func (p *Provider) Status() *v1.StorageStatus {
	status := v1.StorageStatus{
		IsWritable:    false,
		ClusterStatus: v1.ClusterStatus_CLUSTER_NODE,
		Message:       storage.ErrNotStorageNode.Error(),
	}
	config, err := p.getConfiguration()
	if err != nil {
		p.log.Error("Failed to get storage peer configuration", "error", err.Error())
		return &status
	}
	p.log.Debug("Got storage peer configuration", "config", config)
	for _, peer := range config.Servers {
		status.Peers = append(status.Peers, &v1.StoragePeer{
			Id:            peer.GetId(),
			Address:       peer.GetAddress(),
			ClusterStatus: peer.GetSuffrage(),
		})
	}
	return &status
}

func (p *Provider) getConfiguration() (*v1.StorageConfigurationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := p.Options.Dialer.DialNode(ctx, "")
	if err != nil {
		return nil, err
	}
	cli := v1.NewMembershipClient(c)
	config, err := cli.GetStorageConfiguration(ctx, &v1.StorageConfigurationRequest{})
	if err != nil {
		return nil, err
	}
	return config, nil
}

// PassthroughConsensus is a consensus provider that returns an error for all
// write operations.
type Consensus struct {
	*Provider
}

// IsLeader returns true if the node is the leader of the storage group.
func (p *Consensus) IsLeader() bool { return false }

// IsMember returns true if the node is a member of the storage group.
func (p *Consensus) IsMember() bool { return false }

// GetLeader returns the leader of the storage group.
func (p *Consensus) GetLeader(context.Context) (*v1.StoragePeer, error) {
	status := p.Status()
	var leader *v1.StoragePeer
	for _, peer := range status.GetPeers() {
		if peer.GetClusterStatus() == v1.ClusterStatus_CLUSTER_LEADER {
			leader = peer
			break
		}
	}
	if leader == nil {
		p.log.Warn("No leader found in storage group", "status", status)
		return nil, storage.ErrNoLeader
	}
	return leader, nil
}

// AddVoter adds a voter to the consensus group.
func (p *Consensus) AddVoter(context.Context, *v1.StoragePeer) error {
	return storage.ErrNotStorageNode
}

// AddObserver adds an observer to the consensus group.
func (p *Consensus) AddObserver(context.Context, *v1.StoragePeer) error {
	return storage.ErrNotStorageNode
}

// DemoteVoter demotes a voter to an observer.
func (p *Consensus) DemoteVoter(context.Context, *v1.StoragePeer) error {
	return storage.ErrNotStorageNode
}

// RemovePeer removes a peer from the consensus group. If wait
// is true, the function will wait for the peer to be removed.
func (p *Consensus) RemovePeer(ctx context.Context, peer *v1.StoragePeer, wait bool) error {
	return storage.ErrNotStorageNode
}

type Storage struct {
	*Provider
}

// GetValue returns the value of a key.
func (p *Storage) GetValue(ctx context.Context, key string) (string, error) {
	cli, close, err := p.newStorageClient(ctx)
	if err != nil {
		return "", err
	}
	defer close()
	resp, err := cli.Query(ctx, &v1.QueryRequest{
		Command: v1.QueryRequest_GET,
		Query:   key,
	})
	if err != nil {
		return "", err
	}
	defer p.checkErr(resp.CloseSend)
	result, err := resp.Recv()
	if err != nil {
		return "", err
	}
	if result.GetError() != "" {
		// TODO: Should find a way to type assert this error
		if strings.Contains(result.GetError(), storage.ErrKeyNotFound.Error()) {
			return "", storage.NewKeyNotFoundError(key)
		}
		return "", errors.New(result.GetError())
	}
	return result.GetValue()[0], nil
}

// PutValue sets the value of a key. TTL is optional and can be set to 0.
func (p *Storage) PutValue(ctx context.Context, key, value string, ttl time.Duration) error {
	// We pass this through to the publish API. Should only be called by non-raft nodes wanting to publish
	// non-internal values. The server will enforce permissions and other restrictions.
	cli, close, err := p.newStorageClient(ctx)
	if err != nil {
		return err
	}
	defer close()
	_, err = cli.Publish(ctx, &v1.PublishRequest{
		Key:   key,
		Value: value,
		Ttl:   durationpb.New(ttl),
	})
	return err
}

// Delete removes a key.
func (p *Storage) Delete(ctx context.Context, key string) error {
	return storage.ErrNotStorageNode
}

// List returns all keys with a given prefix.
func (p *Storage) List(ctx context.Context, prefix string) ([]string, error) {
	cli, close, err := p.newStorageClient(ctx)
	if err != nil {
		return nil, err
	}
	defer close()
	resp, err := cli.Query(ctx, &v1.QueryRequest{
		Command: v1.QueryRequest_LIST,
		Query:   prefix,
	})
	if err != nil {
		return nil, err
	}
	defer p.checkErr(resp.CloseSend)
	result, err := resp.Recv()
	if err != nil {
		return nil, err
	}
	if result.GetError() != "" {
		// TODO: Should find a way to type assert this error
		return nil, errors.New(result.GetError())
	}
	return result.GetValue(), nil
}

// IterPrefix iterates over all keys with a given prefix. It is important
// that the iterator not attempt any write operations as this will cause
// a deadlock.
func (p *Storage) IterPrefix(ctx context.Context, prefix string, fn storage.PrefixIterator) error {
	cli, close, err := p.newStorageClient(ctx)
	if err != nil {
		return err
	}
	defer close()
	resp, err := cli.Query(ctx, &v1.QueryRequest{
		Command: v1.QueryRequest_ITER,
		Query:   prefix,
	})
	if err != nil {
		return err
	}
	defer p.checkErr(resp.CloseSend)
	for {
		result, err := resp.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if result.GetError() == "EOF" {
			return nil
		}
		if result.GetError() != "" {
			// Should not happen
			return errors.New(result.GetError())
		}
		if err := fn(result.GetKey(), result.GetValue()[0]); err != nil {
			return err
		}
	}
}

// Snapshot returns a snapshot of the storage.
func (p *Storage) Snapshot(ctx context.Context) (io.Reader, error) {
	return nil, storage.ErrNotStorageNode
}

// Restore restores a snapshot of the storage.
func (p *Storage) Restore(ctx context.Context, r io.Reader) error {
	return storage.ErrNotStorageNode
}

// Subscribe will call the given function whenever a key with the given prefix is changed.
// The returned function can be called to unsubscribe.
func (p *Storage) Subscribe(ctx context.Context, prefix string, fn storage.SubscribeFunc) (context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.subCancels = append(p.subCancels, cancel)
	p.mu.Unlock()
	go func() {
		var started bool
		for {
			select {
			case <-p.closec:
				return
			case <-ctx.Done():
				return
			default:
			}
			if !started {
				// Start with a full iteration of the prefix, then switch to a subscription.
				// This is a hack to work around the fact that this method is used to receive
				// peer updates still very early in the startup process.
				err := p.IterPrefix(ctx, prefix, func(key, value string) error {
					fn(key, value)
					return nil
				})
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					p.log.Error("error in storage subscription, retrying in 3 seconds", "error", err.Error())
					select {
					case <-p.closec:
						return
					case <-ctx.Done():
						return
					case <-time.After(3 * time.Second):
					}
					continue
				}
				started = true
			}
			err := p.doSubscribe(ctx, prefix, fn)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				p.log.Error("error in storage subscription, retrying in 3 seconds", "error", err.Error())
				select {
				case <-p.closec:
					return
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			return
		}
	}()
	return cancel, nil
}

func (p *Storage) doSubscribe(ctx context.Context, prefix string, fn storage.SubscribeFunc) error {
	cli, close, err := p.newStorageClient(ctx)
	if err != nil {
		return err
	}
	defer close()
	stream, err := cli.Subscribe(ctx, &v1.SubscribeRequest{
		Prefix: prefix,
	})
	if err != nil {
		return err
	}
	defer p.checkErr(stream.CloseSend)
	for {
		select {
		case <-p.closec:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		res, err := stream.Recv()
		if err != nil {
			return err
		}
		fn(res.GetKey(), res.GetValue())
	}
}

// Close closes the storage. This is a no-op and is handled by the passthroughRaft.
func (p *Storage) Close() error {
	return nil
}

func (p *Storage) newStorageClient(ctx context.Context) (v1.StorageQueryServiceClient, func(), error) {
	select {
	case <-p.closec:
		return nil, nil, storage.ErrClosed
	default:
	}
	c, err := p.Options.Dialer.DialNode(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	return v1.NewStorageQueryServiceClient(c), func() { _ = c.Close() }, nil
}

func (p *Storage) checkErr(fn func() error) {
	if err := fn(); err != nil {
		p.log.Error("error in storage operation", "error", err)
	}
}