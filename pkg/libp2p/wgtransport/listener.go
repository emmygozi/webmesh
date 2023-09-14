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

package wgtransport

import (
	"errors"
	"log/slog"
	"net"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	mnet "github.com/multiformats/go-multiaddr/net"

	"github.com/webmeshproj/webmesh/pkg/context"
	wmcrypto "github.com/webmeshproj/webmesh/pkg/crypto"
	wmproto "github.com/webmeshproj/webmesh/pkg/libp2p/protocol"
	"github.com/webmeshproj/webmesh/pkg/net/wireguard"
)

// WebmeshConn wraps the basic net.Conn with a reference back to the underlying transport.
type WebmeshConn struct {
	mnet.Conn
	rt     *Transport
	lkey   wmcrypto.PrivateKey
	lpeer  peer.ID
	iface  wireguard.Interface
	rmaddr ma.Multiaddr
	eps    []string
	log    *slog.Logger
}

func (w *WebmeshConn) LocalMultiaddr() ma.Multiaddr {
	return wmproto.Encapsulate(w.Conn.LocalMultiaddr(), w.lpeer)
}

func (w *WebmeshConn) RemoteMultiaddr() ma.Multiaddr {
	return w.rmaddr
}

// Context returns a context that contains the logger tied
// to this connection
func (w *WebmeshConn) Context() context.Context {
	return context.WithLogger(context.Background(), w.log)
}

// WebmeshListener wraps a basic listener to be upgraded and injects the transport
// into incoming connections.
type WebmeshListener struct {
	mnet.Listener
	rt    *Transport
	conns chan *WebmeshConn
	donec chan struct{}
}

// Accept waits for and returns the next connection to the listener.
func (ln *WebmeshListener) Accept() (mnet.Conn, error) {
	select {
	case c := <-ln.conns:
		return c, nil
	case <-ln.donec:
		return nil, transport.ErrListenerClosed
	}
}

func (ln *WebmeshListener) Multiaddr() ma.Multiaddr {
	return wmproto.Encapsulate(ln.Listener.Multiaddr(), ln.rt.peerID)
}

func (ln *WebmeshListener) handleIncoming() {
	defer close(ln.donec)
	for {
		c, err := ln.Listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			ln.rt.log.Error("Failed to accept connection", "error", err.Error())
			return
		}
		ln.conns <- ln.rt.WrapConn(c)
	}
}
