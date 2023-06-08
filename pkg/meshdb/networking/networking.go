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

// Package networking contains interfaces to the database models for Network ACLs and Routes.
package networking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "github.com/webmeshproj/api/v1"

	"github.com/webmeshproj/node/pkg/meshdb"
	"github.com/webmeshproj/node/pkg/meshdb/models/raftdb"
)

const (
	// BootstrapNodesNetworkACLName is the name of the bootstrap nodes NetworkACL.
	BootstrapNodesNetworkACLName = "bootstrap-nodes"
)

// IsSystemNetworkACL returns true if the NetworkACL is a system NetworkACL.
func IsSystemNetworkACL(name string) bool {
	return name == BootstrapNodesNetworkACLName
}

// ErrACLNotFound is returned when a NetworkACL is not found.
var ErrACLNotFound = errors.New("network acl not found")

// ErrRouteNotFound is returned when a Route is not found.
var ErrRouteNotFound = errors.New("route not found")

// Networking is the interface to the database models for network resources.
type Networking interface {
	// PutNetworkACL creates or updates a NetworkACL.
	PutNetworkACL(ctx context.Context, acl *v1.NetworkACL) error
	// GetNetworkACL returns a NetworkACL by name.
	GetNetworkACL(ctx context.Context, name string) (*v1.NetworkACL, error)
	// DeleteNetworkACL deletes a NetworkACL by name.
	DeleteNetworkACL(ctx context.Context, name string) error
	// ListNetworkACLs returns a list of NetworkACLs.
	ListNetworkACLs(ctx context.Context) ([]*v1.NetworkACL, error)

	// PutRoute creates or updates a Route.
	PutRoute(ctx context.Context, route *v1.Route) error
	// GetRoute returns a Route by name.
	GetRoute(ctx context.Context, name string) (*v1.Route, error)
	// GetRoutesByNode returns a list of Routes for a given Node.
	GetRoutesByNode(ctx context.Context, nodeName string) ([]*v1.Route, error)
	// GetRoutesByCIDR returns a list of Routes for a given CIDR.
	GetRoutesByCIDR(ctx context.Context, cidr string) ([]*v1.Route, error)
	// DeleteRoute deletes a Route by name.
	DeleteRoute(ctx context.Context, name string) error
	// ListRoutes returns a list of Routes.
	ListRoutes(ctx context.Context) ([]*v1.Route, error)

	// AppendNodeToRoute appends a node to an existing Route.
	AppendNodeToRoute(ctx context.Context, routeName string, nodeName string) error
}

// New returns a new Networking interface.
func New(store meshdb.Store) Networking {
	return &networking{
		store: store,
	}
}

type networking struct {
	store meshdb.Store
}

// PutNetworkACL creates or updates a NetworkACL.
func (n *networking) PutNetworkACL(ctx context.Context, acl *v1.NetworkACL) error {
	if IsSystemNetworkACL(acl.GetName()) {
		return fmt.Errorf("cannot update system network acl %s", acl.GetName())
	}
	q := raftdb.New(n.store.DB())
	params := raftdb.PutNetworkACLParams{
		Name:       acl.GetName(),
		Priority:   int64(acl.GetPriority()),
		Action:     int64(acl.GetAction()),
		SrcNodeIds: sql.NullString{},
		DstNodeIds: sql.NullString{},
		SrcCidrs:   sql.NullString{},
		DstCidrs:   sql.NullString{},
		Protocols:  sql.NullString{},
		Ports:      sql.NullString{},
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if len(acl.GetSourceNodes()) > 0 {
		params.SrcNodeIds = sql.NullString{
			String: strings.Join(acl.GetSourceNodes(), ","),
			Valid:  true,
		}
	}
	if len(acl.GetDestinationNodes()) > 0 {
		params.DstNodeIds = sql.NullString{
			String: strings.Join(acl.GetDestinationNodes(), ","),
			Valid:  true,
		}
	}
	if len(acl.GetSourceCidrs()) > 0 {
		params.SrcCidrs = sql.NullString{
			String: strings.Join(acl.GetSourceCidrs(), ","),
			Valid:  true,
		}
	}
	if len(acl.GetDestinationCidrs()) > 0 {
		params.DstCidrs = sql.NullString{
			String: strings.Join(acl.GetDestinationCidrs(), ","),
			Valid:  true,
		}
	}
	if len(acl.GetProtocols()) > 0 {
		params.Protocols = sql.NullString{
			String: strings.Join(acl.GetProtocols(), ","),
			Valid:  true,
		}
	}
	if len(acl.GetPorts()) > 0 {
		protocols := make([]string, len(acl.GetPorts()))
		for i, port := range acl.GetPorts() {
			protocols[i] = strconv.Itoa(int(port))
		}
		params.Ports = sql.NullString{
			String: strings.Join(protocols, ","),
			Valid:  true,
		}
	}
	err := q.PutNetworkACL(ctx, params)
	if err != nil {
		return fmt.Errorf("put network acl: %w", err)
	}
	return nil
}

// GetNetworkACL returns a NetworkACL by name.
func (n *networking) GetNetworkACL(ctx context.Context, name string) (*v1.NetworkACL, error) {
	q := raftdb.New(n.store.ReadDB())
	acl, err := q.GetNetworkACL(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrACLNotFound
		}
		return nil, fmt.Errorf("get network acl: %w", err)
	}
	return dbACLToAPIACL(&acl), nil
}

// DeleteNetworkACL deletes a NetworkACL by name.
func (n *networking) DeleteNetworkACL(ctx context.Context, name string) error {
	if IsSystemNetworkACL(name) {
		return fmt.Errorf("cannot delete system network acl %s", name)
	}
	q := raftdb.New(n.store.DB())
	err := q.DeleteNetworkACL(ctx, name)
	if err != nil {
		return fmt.Errorf("delete network acl: %w", err)
	}
	return nil
}

// ListNetworkACLs returns a list of NetworkACLs.
func (n *networking) ListNetworkACLs(ctx context.Context) ([]*v1.NetworkACL, error) {
	q := raftdb.New(n.store.ReadDB())
	acls, err := q.ListNetworkACLs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list network acls: %w", err)
	}
	out := make([]*v1.NetworkACL, len(acls))
	for i, acl := range acls {
		out[i] = dbACLToAPIACL(&acl)
	}
	return out, nil
}

// PutRoute creates or updates a Route.
func (n *networking) PutRoute(ctx context.Context, route *v1.Route) error {
	q := raftdb.New(n.store.DB())
	params := raftdb.PutNetworkRouteParams{
		Name:      route.GetName(),
		Nodes:     strings.Join(route.GetNodes(), ","),
		DstCidrs:  strings.Join(route.GetDestinationCidrs(), ","),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if len(route.GetNextHopNodes()) > 0 {
		params.NextHops = sql.NullString{
			String: strings.Join(route.GetNextHopNodes(), ","),
			Valid:  true,
		}
	}
	err := q.PutNetworkRoute(ctx, params)
	if err != nil {
		return fmt.Errorf("put network route: %w", err)
	}
	return nil
}

// GetRoute returns a Route by name.
func (n *networking) GetRoute(ctx context.Context, name string) (*v1.Route, error) {
	q := raftdb.New(n.store.ReadDB())
	route, err := q.GetNetworkRoute(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrRouteNotFound
		}
		return nil, fmt.Errorf("get network route: %w", err)
	}
	return dbRouteToAPIRoute(&route), nil
}

// GetRoutesByNode returns a list of Routes for a given Node.
func (n *networking) GetRoutesByNode(ctx context.Context, nodeName string) ([]*v1.Route, error) {
	q := raftdb.New(n.store.ReadDB())
	routes, err := q.ListNetworkRoutesByNode(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("list network routes by node: %w", err)
	}
	out := make([]*v1.Route, len(routes))
	for i, route := range routes {
		out[i] = dbRouteToAPIRoute(&route)
	}
	return out, nil
}

// GetRoutesByCIDR returns a list of Routes for a given CIDR.
func (n *networking) GetRoutesByCIDR(ctx context.Context, cidr string) ([]*v1.Route, error) {
	q := raftdb.New(n.store.ReadDB())
	routes, err := q.ListNetworkRoutesByDstCidr(ctx, cidr)
	if err != nil {
		return nil, fmt.Errorf("list network routes by cidr: %w", err)
	}
	out := make([]*v1.Route, len(routes))
	for i, route := range routes {
		out[i] = dbRouteToAPIRoute(&route)
	}
	return out, nil
}

// DeleteRoute deletes a Route by name.
func (n *networking) DeleteRoute(ctx context.Context, name string) error {
	q := raftdb.New(n.store.DB())
	err := q.DeleteNetworkRoute(ctx, name)
	if err != nil {
		return fmt.Errorf("delete network route: %w", err)
	}
	return nil
}

// ListRoutes returns a list of Routes.
func (n *networking) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	q := raftdb.New(n.store.ReadDB())
	routes, err := q.ListNetworkRoutes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list network routes: %w", err)
	}
	out := make([]*v1.Route, len(routes))
	for i, route := range routes {
		out[i] = dbRouteToAPIRoute(&route)
	}
	return out, nil
}

// AppendNodeToRoute appends a node to an existing Route.
func (n *networking) AppendNodeToRoute(ctx context.Context, routeName string, nodeName string) error {
	route, err := n.GetRoute(ctx, routeName)
	if err != nil {
		return fmt.Errorf("get route: %w", err)
	}
	for _, node := range route.Nodes {
		if node == nodeName {
			// Node already exists in route. Nothing to do.
			return nil
		}
	}
	route.Nodes = append(route.Nodes, nodeName)
	return n.PutRoute(ctx, route)
}

func dbACLToAPIACL(dbACL *raftdb.NetworkAcl) *v1.NetworkACL {
	return &v1.NetworkACL{
		Name:     dbACL.Name,
		Priority: int32(dbACL.Priority),
		Action:   v1.ACLAction(dbACL.Action),
		SourceNodes: func() []string {
			if dbACL.SrcNodeIds.Valid {
				return strings.Split(dbACL.SrcNodeIds.String, ",")
			}
			return nil
		}(),
		DestinationNodes: func() []string {
			if dbACL.DstNodeIds.Valid {
				return strings.Split(dbACL.DstNodeIds.String, ",")
			}
			return nil
		}(),
		SourceCidrs: func() []string {
			if dbACL.SrcCidrs.Valid {
				return strings.Split(dbACL.SrcCidrs.String, ",")
			}
			return nil
		}(),
		DestinationCidrs: func() []string {
			if dbACL.DstCidrs.Valid {
				return strings.Split(dbACL.DstCidrs.String, ",")
			}
			return nil
		}(),
		Protocols: func() []string {
			if dbACL.Protocols.Valid {
				return strings.Split(dbACL.Protocols.String, ",")
			}
			return nil
		}(),
		Ports: func() []uint32 {
			if dbACL.Ports.Valid {
				out := make([]uint32, 0)
				for _, port := range strings.Split(dbACL.Ports.String, ",") {
					p, err := strconv.ParseUint(port, 10, 32)
					if err != nil {
						// A bad port got into the databse somehow, just skip it
						continue
					}
					out = append(out, uint32(p))
				}
				return out
			}
			return nil
		}(),
	}
}

func dbRouteToAPIRoute(dbRoute *raftdb.NetworkRoute) *v1.Route {
	return &v1.Route{
		Name:             dbRoute.Name,
		Nodes:            strings.Split(dbRoute.Nodes, ","),
		DestinationCidrs: strings.Split(dbRoute.DstCidrs, ","),
		NextHopNodes: func() []string {
			if dbRoute.NextHops.Valid {
				return strings.Split(dbRoute.NextHops.String, ",")
			}
			return nil
		}(),
	}
}