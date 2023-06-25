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

// Package admin provides the admin gRPC server.
package admin

import (
	v1 "github.com/webmeshproj/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/webmeshproj/node/pkg/context"
	"github.com/webmeshproj/node/pkg/services/rbac"
)

var getEdgeAction = rbac.Actions{
	{
		Resource: v1.RuleResource_RESOURCE_EDGES,
		Verb:     v1.RuleVerbs_VERB_GET,
	},
}

func (s *Server) GetEdge(ctx context.Context, edge *v1.MeshEdge) (*v1.MeshEdge, error) {
	if edge.GetSource() == "" {
		return nil, status.Error(codes.InvalidArgument, "edge source is required")
	}
	if edge.GetTarget() == "" {
		return nil, status.Error(codes.InvalidArgument, "edge target is required")
	}
	for _, id := range []string{edge.GetSource(), edge.GetTarget()} {
		if ok, err := s.rbacEval.Evaluate(ctx, getEdgeAction.For(id)); !ok {
			if err != nil {
				context.LoggerFrom(ctx).Error("failed to evaluate put edge action", "error", err)
			}
			return nil, status.Error(codes.PermissionDenied, "caller does not have permission to put the given edge")
		}
	}
	graphEdge, err := s.peers.Graph().Edge(edge.GetSource(), edge.GetTarget())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &v1.MeshEdge{
		Source:     graphEdge.Source.ID,
		Target:     graphEdge.Target.ID,
		Weight:     int32(graphEdge.Properties.Weight),
		Attributes: graphEdge.Properties.Attributes,
	}, nil
}