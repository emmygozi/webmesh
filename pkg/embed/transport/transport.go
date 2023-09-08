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

// Package transport defines the libp2p webmesh transport.
package transport

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	mnet "github.com/multiformats/go-multiaddr/net"
	v1 "github.com/webmeshproj/api/v1"

	"github.com/webmeshproj/webmesh/pkg/cmd/config"
	"github.com/webmeshproj/webmesh/pkg/context"
	"github.com/webmeshproj/webmesh/pkg/crypto"
	"github.com/webmeshproj/webmesh/pkg/embed/protocol"
	"github.com/webmeshproj/webmesh/pkg/embed/security"
	"github.com/webmeshproj/webmesh/pkg/mesh"
	"github.com/webmeshproj/webmesh/pkg/meshdb/peers"
	"github.com/webmeshproj/webmesh/pkg/services"
	"github.com/webmeshproj/webmesh/pkg/util/logutil"
	"github.com/webmeshproj/webmesh/pkg/util/netutil"
)

// ErrInvalidSecureTransport is returned when the transport is not used with a webmesh keypair and security transport.
var ErrInvalidSecureTransport = fmt.Errorf("transport must be used with a webmesh keypair and security transport")

// ErrNotStarted is returned when the transport is not started.
var ErrNotStarted = fmt.Errorf("transport is not started")

// Transport is the webmesh transport.
type Transport interface {
	// Closer for the underlying transport that shuts down the webmesh node.
	io.Closer
	// Transport is the underlying libp2p Transport.
	transport.Transport
	// Resolver is a resolver that uses the mesh storage to lookup peers.
	transport.Resolver
}

// TransportBuilder is the signature of a function that builds a webmesh transport.
type TransportBuilder func(upgrader transport.Upgrader, host host.Host, rcmgr network.ResourceManager, st sec.SecureTransport, mux network.Multiplexer, privKey pcrypto.PrivKey) (Transport, error)

// Options are the options for the webmesh transport.
type Options struct {
	// Config is the webmesh config.
	Config *config.Config
	// StartTimeout is the timeout for starting the webmesh node.
	StartTimeout time.Duration
	// StopTimeout is the timeout for stopping the webmesh node.
	StopTimeout time.Duration
	// ListenTimeout is the timeout for starting a listener on the webmesh node.
	ListenTimeout time.Duration
}

// New returns a new webmesh transport builder.
func New(opts Options) TransportBuilder {
	if opts.Config == nil {
		panic("config is required")
	}
	return func(tu transport.Upgrader, host host.Host, rcmgr network.ResourceManager, st sec.SecureTransport, mux network.Multiplexer, privKey pcrypto.PrivKey) (Transport, error) {
		sec, ok := st.(*security.SecureTransport)
		if !ok {
			return nil, ErrInvalidSecureTransport
		}
		key, ok := privKey.(crypto.PrivateKey)
		if !ok {
			return nil, ErrInvalidSecureTransport
		}
		return &WebmeshTransport{
			opts:  opts,
			node:  nil,
			host:  host,
			key:   key,
			sec:   sec,
			mux:   mux,
			tu:    tu,
			rcmgr: rcmgr,
			log:   logutil.NewLogger(opts.Config.Global.LogLevel).With("component", "webmesh-transport"),
		}, nil
	}
}

// WebmeshTransport is the webmesh libp2p transport. It must be used with a webmesh keypair and security transport.
type WebmeshTransport struct {
	started atomic.Bool
	opts    Options
	node    mesh.Node
	svcs    *services.Server
	host    host.Host
	key     crypto.PrivateKey
	sec     *security.SecureTransport
	mux     network.Multiplexer
	tu      transport.Upgrader
	rcmgr   network.ResourceManager
	log     *slog.Logger
	laddrs  []ma.Multiaddr
	mu      sync.Mutex
}

// Dial dials a remote peer. It should try to reuse local listener
// addresses if possible, but it may choose not to.
func (t *WebmeshTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started.Load() {
		return nil, ErrNotStarted
	}
	var dialer mnet.Dialer
	// If we are dialing a webmesh address, we can compute the IP from their ID
	if _, err := raddr.ValueForProtocol(protocol.P_WEBMESH); err == nil {
		// If we can extract the public key from the peer ID, we know their webmesh address
		// and can dial them directly.
		pubKey, err := crypto.ExtractPublicKey(p)
		if err == nil {
			dport, err := raddr.ValueForProtocol(ma.P_TCP)
			if err != nil {
				return nil, fmt.Errorf("failed to get port from remote address: %w", err)
			}
			rip6 := netutil.AssignToPrefix(t.node.Network().NetworkV6(), pubKey.WireGuardKey()).Addr()
			raddr, err = ma.NewMultiaddr(fmt.Sprintf("/ip6/%s/tcp/%s", rip6, dport))
			if err != nil {
				return nil, fmt.Errorf("failed to create remote address: %w", err)
			}
		}
	}
	c, err := dialer.DialContext(ctx, raddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}
	u, err := t.tu.Upgrade(ctx, t, c, network.DirOutbound, p, connScope)
	if err != nil {
		return nil, fmt.Errorf("failed to upgrade connection: %w", err)
	}
	return u, nil
}

// CanDial returns true if this transport knows how to dial the given
// multiaddr.
//
// Returning true does not guarantee that dialing this multiaddr will
// succeed. This function should *only* be used to preemptively filter
// out addresses that we can't dial.
func (t *WebmeshTransport) CanDial(addr ma.Multiaddr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started.Load() {
		return false
	}
	// For now we say we can dial any webmesh address
	_, err := addr.ValueForProtocol(protocol.P_WEBMESH)
	if err == nil {
		return true
	}
	// Same goes for DNS4/DNS6
	_, err = addr.ValueForProtocol(ma.P_DNS4)
	if err == nil {
		return true
	}
	_, err = addr.ValueForProtocol(ma.P_DNS6)
	if err == nil {
		return true
	}
	// We can do ip4/ip6 dialing if they are within our network.
	ip4addr, err := addr.ValueForProtocol(ma.P_IP4)
	if err == nil {
		addr, err := netip.ParseAddr(ip4addr)
		if err != nil {
			return false
		}
		return t.node.Network().NetworkV4().Contains(addr)
	}
	ip6addr, err := addr.ValueForProtocol(ma.P_IP6)
	if err == nil {
		addr, err := netip.ParseAddr(ip6addr)
		if err != nil {
			return false
		}
		return t.node.Network().NetworkV6().Contains(addr)
	}
	return false
}

// Listen listens on the passed multiaddr.
func (t *WebmeshTransport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx := context.Background()
	if t.opts.ListenTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.opts.ListenTimeout)
		defer cancel()
	}
	if !t.started.Load() {
		// We use the background context to not let the listen timeout
		// interfere with the start timeout
		node, err := t.startNode(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to start node: %w", err)
		}
		t.node = node
		t.sec.SetInterface(node.Network().WireGuard())
		t.started.Store(true)
	}
	// Find the port requested in the listener address
	port, err := laddr.ValueForProtocol(ma.P_TCP)
	if err != nil {
		return nil, fmt.Errorf("failed to get port from listener address: %w", err)
	}
	// We automatically set the listening address to our local IPv6 address
	lnetaddr := t.node.Network().WireGuard().AddressV6().Addr()
	laddr, err = ma.NewMultiaddr(fmt.Sprintf("/ip6/%s/tcp/%s", lnetaddr, port))
	if err != nil {
		return nil, fmt.Errorf("failed to create listener address: %w", err)
	}
	// TODO: Support IPv4
	t.log.Info("Listening for webmesh connections", "address", laddr.String())
	lis, err := mnet.Listen(laddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}
	// Register our listeners to the webmesh cluster so they can help
	// others find us
	addrs, err := t.multiaddrsForListener(lis.Addr())
	if err != nil {
		defer lis.Close()
		return nil, fmt.Errorf("failed to get multiaddrs for listener: %w", err)
	}
	err = t.registerMultiaddrs(ctx, addrs)
	if err != nil {
		defer lis.Close()
		return nil, fmt.Errorf("failed to register multiaddrs: %w", err)
	}
	return t.tu.UpgradeListener(t, lis), nil
}

// Resolve attempts to resolve the given multiaddr to a list of addresses.
func (t *WebmeshTransport) Resolve(ctx context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started.Load() {
		return nil, ErrNotStarted
	}
	// If it's a DNS address, we can try using our local MeshDNS to resolve it.
	value, err := maddr.ValueForProtocol(ma.P_DNS6)
	if err == nil {
		fqdn := strings.TrimSuffix(value, ".")
		nodeID := strings.TrimSuffix(fqdn, "."+t.node.Domain())
		if nodeID == t.node.ID() {
			return nil, fmt.Errorf("cannot resolve self")
		}
		peer, err := peers.New(t.node.Storage()).Get(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to get peer: %w", err)
		}
		return peerToIPMultiaddrs(peer, maddr)
	}
	// We can do the same for DNS4 addresses
	value, err = maddr.ValueForProtocol(ma.P_DNS4)
	if err == nil {
		fqdn := strings.TrimSuffix(value, ".")
		nodeID := strings.TrimSuffix(fqdn, "."+t.node.Domain())
		if nodeID == t.node.ID() {
			return nil, fmt.Errorf("cannot resolve self")
		}
		peer, err := peers.New(t.node.Storage()).Get(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to get peer: %w", err)
		}
		return peerToIPMultiaddrs(peer, maddr)
	}
	// If we have a webmesh protocol, we can resolve the peer ID
	id, err := protocol.PeerIDFromWebmeshAddr(maddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get any webmesh protocol value: %w", err)
	}
	pubkey, err := crypto.ExtractPublicKey(id)
	if err != nil {
		return nil, fmt.Errorf("failed to extract public key: %w", err)
	}
	peer, err := peers.New(t.node.Storage()).GetByPubKey(ctx, pubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer: %w", err)
	}
	return peerToIPMultiaddrs(peer, maddr)
}

// Protocol returns the set of protocols handled by this transport.
func (t *WebmeshTransport) Protocols() []int {
	return []int{protocol.Code}
}

// Proxy returns true if this is a proxy transport.
func (t *WebmeshTransport) Proxy() bool {
	return true
}

// Close closes the transport.
func (t *WebmeshTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started.Load() {
		return ErrNotStarted
	}
	defer t.started.Store(false)
	ctx := context.Background()
	if t.opts.StopTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.opts.StopTimeout)
		defer cancel()
	}
	if t.svcs != nil {
		defer t.svcs.Shutdown(ctx)
	}
	return t.node.Close(ctx)
}

func (t *WebmeshTransport) startNode(ctx context.Context) (mesh.Node, error) {
	if t.opts.StartTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.opts.StartTimeout)
		defer cancel()
	}
	conf := t.opts.Config.ShallowCopy()
	conf.Mesh.NodeID = t.key.ID().String()
	err := conf.Validate()
	if err != nil {
		return nil, fmt.Errorf("failed to validate config: %w", err)
	}

	// Build out everything we need for a new node
	meshConfig, err := conf.NewMeshConfig(ctx, t.key)
	if err != nil {
		return nil, fmt.Errorf("failed to create mesh config: %w", err)
	}
	node := mesh.NewWithLogger(t.log, meshConfig)
	startOpts, err := conf.NewRaftStartOptions(node)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft start options: %w", err)
	}
	raft, err := conf.NewRaftNode(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft node: %w", err)
	}
	connectOpts, err := conf.NewConnectOptions(ctx, node, raft, t.host)
	if err != nil {
		return nil, fmt.Errorf("failed to create connect options: %w", err)
	}

	// Define cleanup handlers
	var cleanFuncs []func() error
	handleErr := func(cause error) error {
		for _, clean := range cleanFuncs {
			if err := clean(); err != nil {
				t.log.Warn("failed to clean up", "error", err.Error())
			}
		}
		return cause
	}

	t.log.Info("Starting webmesh node")
	err = raft.Start(ctx, startOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to start raft node: %w", err)
	}
	cleanFuncs = append(cleanFuncs, func() error {
		return raft.Stop(ctx)
	})
	err = node.Connect(ctx, connectOpts)
	if err != nil {
		return nil, handleErr(fmt.Errorf("failed to connect to mesh: %w", err))
	}
	cleanFuncs = append(cleanFuncs, func() error {
		return node.Close(ctx)
	})

	// Start any mesh services
	srvOpts, err := conf.NewServiceOptions(ctx, node)
	if err != nil {
		return nil, handleErr(fmt.Errorf("failed to create service options: %w", err))
	}
	t.svcs, err = services.NewServer(ctx, srvOpts)
	if err != nil {
		return nil, handleErr(fmt.Errorf("failed to create mesh services: %w", err))
	}
	if !conf.Services.API.Disabled {
		err = conf.RegisterAPIs(ctx, node, t.svcs)
		if err != nil {
			return nil, handleErr(fmt.Errorf("failed to register APIs: %w", err))
		}
	}
	errs := make(chan error, 1)
	go func() {
		t.log.Info("Starting webmesh services")
		if err := t.svcs.ListenAndServe(); err != nil {
			errs <- fmt.Errorf("start mesh services %w", err)
		}
	}()

	// Wait for the node to be ready
	t.log.Info("Waiting for webmesh node to be ready")
	select {
	case <-node.Ready():
	case err := <-errs:
		return nil, handleErr(err)
	case <-ctx.Done():
		return nil, handleErr(fmt.Errorf("failed to start mesh node: %w", ctx.Err()))
	}
	t.log.Info("Webmesh node is ready")
	return node, nil
}

func (t *WebmeshTransport) multiaddrsForListener(listenAddr net.Addr) ([]ma.Multiaddr, error) {
	var lisaddr ma.Multiaddr
	var ip netip.Addr
	var err error
	switch v := listenAddr.(type) {
	case *net.TCPAddr:
		ip = v.AddrPort().Addr()
		lisaddr, err = ma.NewMultiaddr(fmt.Sprintf("/tcp/%d", v.Port))
	case *net.UDPAddr:
		ip = v.AddrPort().Addr()
		lisaddr, err = ma.NewMultiaddr(fmt.Sprintf("/udp/%d", v.Port))
	default:
		err = fmt.Errorf("unknown listener type: %T", listenAddr)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create listener multiaddr: %w", err)
	}
	// Append the webmesh protocol ID and arguments to the listener address
	secPath := fmt.Sprintf("/webmesh/%s", t.node.ID())
	if t.opts.Config.Discovery.Announce {
		// Append the rendezvous string to the listener address
		secPath += fmt.Sprintf("/%s", t.opts.Config.Discovery.PSK)
	}
	secaddr, err := ma.NewMultiaddr(secPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create security multiaddr: %w", err)
	}
	// Produces: /tcp/1234/webmesh/<node-id>/<rendezvous>
	var addrs []ma.Multiaddr
	if ip.Is4() {
		ip4addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s", ip))
		if err != nil {
			return nil, fmt.Errorf("failed to create ip4 multiaddr: %w", err)
		}
		addrs = append(addrs, ma.Join(ip4addr, lisaddr, secaddr))
	}
	if ip.Is6() {
		ip6addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip6/%s", ip))
		if err != nil {
			return nil, fmt.Errorf("failed to create ip6 multiaddr: %w", err)
		}
		addrs = append(addrs, ma.Join(ip6addr, lisaddr, secaddr))
	}
	dnsaddr, err := ma.NewMultiaddr(fmt.Sprintf("/dns6/%s.%s", t.node.ID(), strings.TrimSuffix(t.node.Domain(), ".")))
	if err != nil {
		return nil, fmt.Errorf("failed to create domain multiaddr: %w", err)
	}
	addrs = append(addrs, ma.Join(dnsaddr, lisaddr, secaddr))
	dnsaddr, err = ma.NewMultiaddr(fmt.Sprintf("/dns4/%s.%s", t.node.ID(), strings.TrimSuffix(t.node.Domain(), ".")))
	if err != nil {
		return nil, fmt.Errorf("failed to create domain multiaddr: %w", err)
	}
	addrs = append(addrs, ma.Join(dnsaddr, lisaddr, secaddr))
	return addrs, nil
}

func (t *WebmeshTransport) registerMultiaddrs(ctx context.Context, maddrs []ma.Multiaddr) error {
	t.laddrs = append(t.laddrs, maddrs...)
	// TODO: This needs to include already registered multiaddrs
	addrstrs := func() []string {
		var straddrs []string
		for _, addr := range t.laddrs {
			straddrs = append(straddrs, addr.String())
		}
		return straddrs
	}()
	if !t.node.Raft().IsVoter() {
		c, err := t.node.DialLeader(ctx)
		if err != nil {
			return fmt.Errorf("failed to dial leader: %w", err)
		}
		defer c.Close()
		_, err = v1.NewMembershipClient(c).Update(ctx, &v1.UpdateRequest{
			Id:         t.node.ID(),
			Multiaddrs: addrstrs,
		})
		if err != nil {
			return fmt.Errorf("failed to update membership: %w", err)
		}
		return nil
	}
	// We can write it directly to storage
	self, err := peers.New(t.node.Storage()).Get(ctx, t.node.ID())
	if err != nil {
		return fmt.Errorf("failed to get self: %w", err)
	}
	self.Multiaddrs = addrstrs
	err = peers.New(t.node.Storage()).Put(ctx, self.MeshNode)
	if err != nil {
		return fmt.Errorf("failed to update self: %w", err)
	}
	return nil
}

func peerToIPMultiaddrs(peer peers.MeshNode, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	var peerv4addr, peerv6addr netip.Prefix
	var err error
	if peer.PrivateIpv4 != "" {
		peerv4addr, err = netip.ParsePrefix(peer.PrivateIpv4)
		if err != nil {
			return nil, fmt.Errorf("failed to parse peer address: %w", err)
		}
	}
	if peer.PrivateIpv6 != "" {
		peerv6addr, err = netip.ParsePrefix(peer.PrivateIpv6)
		if err != nil {
			return nil, fmt.Errorf("failed to parse peer address: %w", err)
		}
	}
	port, err := maddr.ValueForProtocol(ma.P_TCP)
	if err != nil {
		return nil, fmt.Errorf("failed to get port from multiaddr: %w", err)
	}
	var addrs []ma.Multiaddr
	if peerv4addr.IsValid() {
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", peerv4addr.Addr(), port))
		if err != nil {
			return nil, fmt.Errorf("failed to create multiaddr: %w", err)
		}
		addrs = append(addrs, addr)
	}
	if peerv6addr.IsValid() {
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip6/%s/tcp/%s", peerv6addr.Addr(), port))
		if err != nil {
			return nil, fmt.Errorf("failed to create multiaddr: %w", err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}