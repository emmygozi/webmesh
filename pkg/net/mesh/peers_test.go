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

package mesh

import (
	"context"
	"net/netip"
	"reflect"
	"sort"
	"testing"

	v1 "github.com/webmeshproj/api/v1"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/webmeshproj/node/pkg/meshdb"
	"github.com/webmeshproj/node/pkg/meshdb/networking"
	"github.com/webmeshproj/node/pkg/meshdb/peers"
)

func TestWireGuardPeers(t *testing.T) {
	tt := []struct {
		name    string
		peers   map[string][]netip.Prefix            // peerID -> addressv4 + addressv6
		edges   map[string][]string                  // peerID -> []peerID
		wantIPs map[string]map[string][]netip.Prefix // peerID -> peerID -> []allowed ips
	}{
		{
			name: "simple 1-to-1",
			peers: map[string][]netip.Prefix{
				"peer1": {
					netip.MustParsePrefix("172.16.0.1/32"),
					netip.MustParsePrefix("2001:db8::1/128"),
				},
				"peer2": {
					netip.MustParsePrefix("172.16.0.2/32"),
					netip.MustParsePrefix("2001:db8::2/128"),
				},
			},
			edges: map[string][]string{
				"peer1": {"peer2"},
			},
			wantIPs: map[string]map[string][]netip.Prefix{
				"peer1": {
					"peer2": {
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
					},
				},
				"peer2": {
					"peer1": {
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
					},
				},
			},
		},
		{
			name: "simple 1-to-1-to-1",
			peers: map[string][]netip.Prefix{
				"peer1": {
					netip.MustParsePrefix("172.16.0.1/32"),
					netip.MustParsePrefix("2001:db8::1/128"),
				},
				"peer2": {
					netip.MustParsePrefix("172.16.0.2/32"),
					netip.MustParsePrefix("2001:db8::2/128"),
				},
				"peer3": {
					netip.MustParsePrefix("172.16.0.3/32"),
					netip.MustParsePrefix("2001:db8::3/128"),
				},
			},
			edges: map[string][]string{
				"peer1": {"peer2", "peer3"},
				"peer2": {"peer1", "peer3"},
				"peer3": {"peer1", "peer2"},
			},
			wantIPs: map[string]map[string][]netip.Prefix{
				"peer1": {
					"peer2": {
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
					},
					"peer3": {
						netip.MustParsePrefix("172.16.0.3/32"),
						netip.MustParsePrefix("2001:db8::3/128"),
					},
				},
				"peer2": {
					"peer1": {
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
					},
					"peer3": {
						netip.MustParsePrefix("172.16.0.3/32"),
						netip.MustParsePrefix("2001:db8::3/128"),
					},
				},
				"peer3": {
					"peer1": {
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
					},
					"peer2": {
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
					},
				},
			},
		},
		{
			name: "simple site-to-site",
			peers: map[string][]netip.Prefix{
				"site1-router": {
					netip.MustParsePrefix("172.16.0.1/32"),
					netip.MustParsePrefix("2001:db8::1/128"),
				},
				"site2-router": {
					netip.MustParsePrefix("172.16.0.2/32"),
					netip.MustParsePrefix("2001:db8::2/128"),
				},
				"site1-follower": {
					netip.MustParsePrefix("172.16.0.3/32"),
					netip.MustParsePrefix("2001:db8::3/128"),
				},
				"site2-follower": {
					netip.MustParsePrefix("172.16.0.4/32"),
					netip.MustParsePrefix("2001:db8::4/128"),
				},
			},
			edges: map[string][]string{
				"site1-router": {"site2-router", "site1-follower"},
				"site2-router": {"site1-router", "site2-follower"},
			},
			wantIPs: map[string]map[string][]netip.Prefix{
				"site1-router": {
					"site2-router": {
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
						// site2-follower is reachable via site2-router
						netip.MustParsePrefix("172.16.0.4/32"),
						netip.MustParsePrefix("2001:db8::4/128"),
					},
					"site1-follower": {
						netip.MustParsePrefix("172.16.0.3/32"),
						netip.MustParsePrefix("2001:db8::3/128"),
					},
				},
				"site2-router": {
					"site1-router": {
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
						// site1-follower is reachable via site1-router
						netip.MustParsePrefix("172.16.0.3/32"),
						netip.MustParsePrefix("2001:db8::3/128"),
					},
					"site2-follower": {
						netip.MustParsePrefix("172.16.0.4/32"),
						netip.MustParsePrefix("2001:db8::4/128"),
					},
				},
				"site1-follower": {
					"site1-router": {
						// All IPs reachable via site1-router
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
						netip.MustParsePrefix("172.16.0.4/32"),
						netip.MustParsePrefix("2001:db8::4/128"),
					},
				},
				"site2-follower": {
					"site2-router": {
						// All IPs reachable via site2-router
						netip.MustParsePrefix("172.16.0.1/32"),
						netip.MustParsePrefix("2001:db8::1/128"),
						netip.MustParsePrefix("172.16.0.2/32"),
						netip.MustParsePrefix("2001:db8::2/128"),
						netip.MustParsePrefix("172.16.0.3/32"),
						netip.MustParsePrefix("2001:db8::3/128"),
					},
				},
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, teardown, err := meshdb.NewTestDB()
			if err != nil {
				t.Fatalf("create test db: %v", err)
			}
			peerdb := peers.New(db)
			nw := networking.New(db)
			// Create an allow-all traffic policy.
			err = nw.PutNetworkACL(ctx, &v1.NetworkACL{
				Name:             "allow-all",
				Action:           v1.ACLAction_ACTION_ACCEPT,
				SourceNodes:      []string{"*"},
				DestinationNodes: []string{"*"},
				SourceCidrs:      []string{"*"},
				DestinationCidrs: []string{"*"},
			})
			if err != nil {
				t.Fatalf("put network acl: %v", err)
			}
			for peerID, addrs := range tc.peers {
				_, err = peerdb.Put(ctx, &peers.PutOptions{
					ID:        peerID,
					PublicKey: mustGenerateKey(t).PublicKey(),
				})
				if err != nil {
					t.Fatalf("put peer %q: %v", peerID, err)
				}
				err = peerdb.PutLease(ctx, &peers.PutLeaseOptions{
					ID:   peerID,
					IPv4: addrs[0],
					IPv6: addrs[1],
				})
				if err != nil {
					t.Fatalf("put lease for peer %q: %v", peerID, err)
				}
			}
			for peerID, edges := range tc.edges {
				for _, edge := range edges {
					err = peerdb.PutEdge(ctx, peers.Edge{
						From: peerID,
						To:   edge,
					})
					if err != nil {
						t.Fatalf("put edge from %q to %q: %v", peerID, edge, err)
					}
				}
			}
			for peer, want := range tc.wantIPs {
				peers, err := WireGuardPeersFor(ctx, db, peer)
				if err != nil {
					t.Fatalf("get peers for %q: %v", peer, err)
				}
				got := make(map[string][]netip.Prefix)
				for node, ips := range want {
					want[node] = sortPrefixes(ips)
				}
				for _, p := range peers {
					var ips []netip.Prefix
					for _, ip := range p.AllowedIps {
						ips = append(ips, netip.MustParsePrefix(ip))
					}
					got[p.Id] = sortPrefixes(ips)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("got %v, want %v", got, want)
				}
			}
			teardown()
		})
	}
}

func sortPrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}

func mustGenerateKey(t *testing.T) wgtypes.Key {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
