/*
Copyright 2023.

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

// Package svcutil contains common utilities for services.
package svcutil

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	v1 "github.com/webmeshproj/api/v1"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/webmeshproj/node/pkg/meshdb"
	"github.com/webmeshproj/node/pkg/meshdb/networking"
	"github.com/webmeshproj/node/pkg/meshdb/peers"
)

// PeerFromContext returns the peer ID from the context.
func PeerFromContext(ctx context.Context) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if ok {
		if authInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			peerCerts := authInfo.State.PeerCertificates
			if len(peerCerts) > 0 {
				return peerCerts[0].Subject.CommonName, true
			}
		}
	}
	return "", false
}

// WireGuardPeersFor returns the WireGuard peers for the given peer ID.
// Peers are filtered by network ACLs.
func WireGuardPeersFor(ctx context.Context, store meshdb.Store, peerID string) ([]*v1.WireGuardPeer, error) {
	graph := peers.New(store).Graph()
	nw := networking.New(store)
	adjacencyMap, err := nw.FilterGraph(ctx, graph, peerID)
	if err != nil {
		return nil, fmt.Errorf("filter adjacency map: %w", err)
	}
	routes, err := nw.GetRoutesByNode(ctx, peerID)
	if err != nil {
		return nil, fmt.Errorf("get routes by node: %w", err)
	}
	ourRoutes := make([]netip.Prefix, 0)
	for _, route := range routes {
		for _, cidr := range route.GetDestinationCidrs() {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil {
				return nil, fmt.Errorf("parse prefix %q: %w", cidr, err)
			}
			ourRoutes = append(ourRoutes, prefix)
		}
	}
	directAdjacents := adjacencyMap[peerID]
	out := make([]*v1.WireGuardPeer, 0, len(directAdjacents))
	for adjacent := range directAdjacents {
		node, err := graph.Vertex(adjacent)
		if err != nil {
			return nil, fmt.Errorf("get vertex: %w", err)
		}
		// Determine the preferred wireguard endpoint
		var primaryEndpoint string
		if node.PrimaryEndpoint != "" {
			for _, endpoint := range node.WireGuardEndpoints {
				if strings.HasPrefix(endpoint, node.PrimaryEndpoint) {
					primaryEndpoint = endpoint
					break
				}
			}
		}
		if primaryEndpoint == "" && len(node.WireGuardEndpoints) > 0 {
			primaryEndpoint = node.WireGuardEndpoints[0]
		}
		// Each direct adjacent is a peer
		peer := &v1.WireGuardPeer{
			Id:                 node.ID,
			PublicKey:          node.PublicKey.String(),
			ZoneAwarenessId:    node.ZoneAwarenessID,
			PrimaryEndpoint:    primaryEndpoint,
			WireguardEndpoints: node.WireGuardEndpoints,
			AddressIpv4: func() string {
				if node.PrivateIPv4.IsValid() {
					return node.PrivateIPv4.String()
				}
				return ""
			}(),
			AddressIpv6: func() string {
				if node.NetworkIPv6.IsValid() {
					return node.NetworkIPv6.String()
				}
				return ""
			}(),
		}
		allowedIPs, allowedRoutes, err := recursePeers(ctx, nw, graph, adjacencyMap, peerID, ourRoutes, &node)
		if err != nil {
			return nil, fmt.Errorf("recurse allowed IPs: %w", err)
		}
		var ourAllowedIPs []string
		for _, ip := range allowedIPs {
			ourAllowedIPs = append(ourAllowedIPs, ip.String())
		}
		var ourAllowedRoutes []string
		for _, route := range allowedRoutes {
			ourAllowedRoutes = append(ourAllowedRoutes, route.String())
		}
		peer.AllowedIps = ourAllowedIPs
		peer.AllowedRoutes = ourAllowedRoutes
		out = append(out, peer)
	}
	return out, nil
}

func recursePeers(
	ctx context.Context,
	nw networking.Networking,
	graph peers.Graph,
	adjacencyMap networking.AdjacencyMap,
	thisPeer string,
	thisRoutes []netip.Prefix,
	node *peers.Node,
) (allowedIPs, allowedRoutes []netip.Prefix, err error) {
	if node.PrivateIPv4.IsValid() {
		allowedIPs = append(allowedIPs, node.PrivateIPv4)
	}
	if node.NetworkIPv6.IsValid() {
		allowedIPs = append(allowedIPs, node.NetworkIPv6)
	}
	// Does this peer expose routes?
	routes, err := nw.GetRoutesByNode(ctx, node.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("get routes by node: %w", err)
	}
	if len(routes) > 0 {
		for _, route := range routes {
			for _, cidr := range route.GetDestinationCidrs() {
				prefix, err := netip.ParsePrefix(cidr)
				if err != nil {
					return nil, nil, fmt.Errorf("parse prefix: %w", err)
				}
				if !contains(allowedIPs, prefix) && !contains(thisRoutes, prefix) {
					allowedIPs = append(allowedIPs, prefix)
				}
			}
		}
	} else if len(thisRoutes) > 0 {
		// The peer doesn't expose routes but we do, so we need to add our routes to the peer
		// TODO: There is a third condition where we both expose routes and there are non-overlapping routes
		for _, prefix := range thisRoutes {
			if !contains(allowedRoutes, prefix) {
				allowedRoutes = append(allowedRoutes, prefix)
			}
		}
	}
	edgeIPs, edgeRoutes, err := recurseEdges(ctx, nw, graph, adjacencyMap, thisPeer, thisRoutes, node, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("recurse edge allowed IPs: %w", err)
	}
	for _, ip := range edgeIPs {
		if !contains(allowedIPs, ip) {
			allowedIPs = append(allowedIPs, ip)
		}
	}
	for _, route := range edgeRoutes {
		if !contains(allowedRoutes, route) {
			allowedRoutes = append(allowedRoutes, route)
		}
	}
	return
}

func recurseEdges(
	ctx context.Context,
	nw networking.Networking,
	graph peers.Graph,
	adjacencyMap networking.AdjacencyMap,
	thisPeer string,
	thisRoutes []netip.Prefix,
	node *peers.Node,
	visited map[string]struct{},
) (allowedIPs, allowedRoutes []netip.Prefix, err error) {
	if visited == nil {
		visited = make(map[string]struct{})
	}
	directAdjacents := adjacencyMap[thisPeer]
	visited[node.ID] = struct{}{}
	targets := adjacencyMap[node.ID]
	for target := range targets {
		if target == thisPeer {
			continue
		}
		if _, ok := directAdjacents[target]; ok {
			continue
		}
		if _, ok := visited[target]; ok {
			continue
		}
		targetNode, err := graph.Vertex(target)
		if err != nil {
			return nil, nil, fmt.Errorf("get vertex: %w", err)
		}
		if targetNode.PrivateIPv4.IsValid() {
			allowedIPs = append(allowedIPs, targetNode.PrivateIPv4)
		}
		if targetNode.NetworkIPv6.IsValid() {
			allowedIPs = append(allowedIPs, targetNode.NetworkIPv6)
		}
		// Does this peer expose routes?
		routes, err := nw.GetRoutesByNode(ctx, targetNode.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("get routes by node: %w", err)
		}
		if len(routes) > 0 {
			for _, route := range routes {
				for _, cidr := range route.GetDestinationCidrs() {
					prefix, err := netip.ParsePrefix(cidr)
					if err != nil {
						return nil, nil, fmt.Errorf("parse prefix: %w", err)
					}
					if !contains(allowedIPs, prefix) && !contains(thisRoutes, prefix) {
						allowedIPs = append(allowedIPs, prefix)
					}
				}
			}
		} else if len(thisRoutes) > 0 {
			// The peer doesn't expose routes but we do, so we need to add our routes to the peer
			for _, prefix := range thisRoutes {
				if !contains(allowedRoutes, prefix) {
					allowedRoutes = append(allowedRoutes, prefix)
				}
			}
		}
		visited[target] = struct{}{}
		ips, ipRoutes, err := recurseEdges(ctx, nw, graph, adjacencyMap, thisPeer, thisRoutes, &targetNode, visited)
		if err != nil {
			return nil, nil, fmt.Errorf("recurse allowed IPs: %w", err)
		}
		for _, ip := range ips {
			if !contains(allowedIPs, ip) {
				allowedIPs = append(allowedIPs, ip)
			}
		}
		for _, ipRoute := range ipRoutes {
			if !contains(allowedRoutes, ipRoute) {
				allowedRoutes = append(allowedRoutes, ipRoute)
			}
		}
	}
	return
}
func contains(ss []netip.Prefix, s netip.Prefix) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}