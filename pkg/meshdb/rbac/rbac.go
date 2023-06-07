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

// Package rbac contains interfaces to the database models for RBAC.
package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	v1 "github.com/webmeshproj/api/v1"

	"github.com/webmeshproj/node/pkg/meshdb"
	"github.com/webmeshproj/node/pkg/meshdb/models/raftdb"
)

// RBAC is the interface to the database models for RBAC.
type RBAC interface {
	// PutRole creates or updates a role.
	PutRole(ctx context.Context, role *v1.Role) error
	// GetRole returns a role by name.
	GetRole(ctx context.Context, name string) (*v1.Role, error)
	// DeleteRole deletes a role by name.
	DeleteRole(ctx context.Context, name string) error
	// ListRoles returns a list of all roles.
	ListRoles(ctx context.Context) (RolesList, error)

	// PutRoleBinding creates or updates a rolebinding.
	PutRoleBinding(ctx context.Context, rolebinding *v1.RoleBinding) error
	// GetRoleBinding returns a rolebinding by name.
	GetRoleBinding(ctx context.Context, name string) (*v1.RoleBinding, error)
	// DeleteRoleBinding deletes a rolebinding by name.
	DeleteRoleBinding(ctx context.Context, name string) error
	// ListRoleBindings returns a list of all rolebindings.
	ListRoleBindings(ctx context.Context) ([]*v1.RoleBinding, error)

	// ListNodeRoles returns a list of all roles for a node.
	ListNodeRoles(ctx context.Context, nodeID string) (RolesList, error)
	// ListUserRoles returns a list of all roles for a user.
	ListUserRoles(ctx context.Context, user string) (RolesList, error)
}

// ErrRoleNotFound is returned when a role is not found.
var ErrRoleNotFound = fmt.Errorf("role not found")

// ErrRoleBindingNotFound is returned when a rolebinding is not found.
var ErrRoleBindingNotFound = fmt.Errorf("rolebinding not found")

// New returns a new RBAC.
func New(store meshdb.Store) RBAC {
	return &rbac{
		store: store,
	}
}

type rbac struct {
	store meshdb.Store
}

// PutRole creates or updates a role.
func (r *rbac) PutRole(ctx context.Context, role *v1.Role) error {
	q := raftdb.New(r.store.DB())
	rules, err := json.Marshal(role.GetRules())
	if err != nil {
		return err
	}
	params := raftdb.PutRoleParams{
		Name:      role.GetName(),
		RulesJson: string(rules),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	err = q.PutRole(ctx, params)
	if err != nil {
		return fmt.Errorf("put db role: %w", err)
	}
	return nil
}

// GetRole returns a role by name.
func (r *rbac) GetRole(ctx context.Context, name string) (*v1.Role, error) {
	role, err := raftdb.New(r.store.ReadDB()).GetRole(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("get db role: %w", err)
	}
	return dbRoleToAPIRole(&role)
}

// DeleteRole deletes a role by name.
func (r *rbac) DeleteRole(ctx context.Context, name string) error {
	q := raftdb.New(r.store.DB())
	err := q.DeleteRole(ctx, name)
	if err != nil {
		return fmt.Errorf("delete db role: %w", err)
	}
	return nil
}

// ListRoles returns a list of all roles.
func (r *rbac) ListRoles(ctx context.Context) (RolesList, error) {
	roles, err := raftdb.New(r.store.ReadDB()).ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list db roles: %w", err)
	}
	out := make(RolesList, len(roles))
	for i, role := range roles {
		out[i], err = dbRoleToAPIRole(&role)
		if err != nil {
			return nil, fmt.Errorf("convert db role: %w", err)
		}
	}
	return out, nil
}

// PutRoleBinding creates or updates a rolebinding.
func (r *rbac) PutRoleBinding(ctx context.Context, rolebinding *v1.RoleBinding) error {
	q := raftdb.New(r.store.DB())
	params := raftdb.PutRoleBindingParams{
		Name:       rolebinding.GetName(),
		RoleName:   rolebinding.GetRole(),
		NodeIds:    sql.NullString{},
		UserNames:  sql.NullString{},
		GroupNames: sql.NullString{},
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	var users, groups, nodes []string
	for _, subject := range rolebinding.GetSubjects() {
		switch subject.GetType() {
		case v1.SubjectType_SUBJECT_NODE:
			nodes = append(nodes, subject.GetName())
		case v1.SubjectType_SUBJECT_USER:
			users = append(users, subject.GetName())
		case v1.SubjectType_SUBJECT_GROUP:
			groups = append(groups, subject.GetName())
		case v1.SubjectType_SUBJECT_ALL:
			nodes = append(nodes, subject.GetName())
			users = append(users, subject.GetName())
			groups = append(groups, subject.GetName())
		}
	}
	if len(nodes) > 0 {
		params.NodeIds = sql.NullString{Valid: true, String: strings.Join(nodes, ",")}
	}
	if len(users) > 0 {
		params.UserNames = sql.NullString{Valid: true, String: strings.Join(users, ",")}
	}
	if len(groups) > 0 {
		params.GroupNames = sql.NullString{Valid: true, String: strings.Join(groups, ",")}
	}
	err := q.PutRoleBinding(ctx, params)
	if err != nil {
		return fmt.Errorf("put db rolebinding: %w", err)
	}
	return nil
}

// GetRoleBinding returns a rolebinding by name.
func (r *rbac) GetRoleBinding(ctx context.Context, name string) (*v1.RoleBinding, error) {
	q := raftdb.New(r.store.ReadDB())
	rb, err := q.GetRoleBinding(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrRoleBindingNotFound
		}
		return nil, fmt.Errorf("get db rolebinding: %w", err)
	}
	return dbRoleBindingToAPIRoleBinding(&rb), nil
}

// DeleteRoleBinding deletes a rolebinding by name.
func (r *rbac) DeleteRoleBinding(ctx context.Context, name string) error {
	q := raftdb.New(r.store.DB())
	err := q.DeleteRoleBinding(ctx, name)
	if err != nil {
		return fmt.Errorf("delete db rolebinding: %w", err)
	}
	return nil
}

// ListRoleBindings returns a list of all rolebindings.
func (r *rbac) ListRoleBindings(ctx context.Context) ([]*v1.RoleBinding, error) {
	q := raftdb.New(r.store.ReadDB())
	rbs, err := q.ListRoleBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list db rolebindings: %w", err)
	}
	out := make([]*v1.RoleBinding, len(rbs))
	for i, rb := range rbs {
		out[i] = dbRoleBindingToAPIRoleBinding(&rb)
	}
	return out, nil
}

// ListNodeRoles returns a list of all roles for a node.
func (r *rbac) ListNodeRoles(ctx context.Context, nodeID string) (RolesList, error) {
	roles, err := raftdb.New(r.store.ReadDB()).ListBoundRolesForNode(ctx, raftdb.ListBoundRolesForNodeParams{
		NodeIds: sql.NullString{
			String: nodeID,
			Valid:  true,
		},
		Nodes: sql.NullString{
			String: nodeID,
			Valid:  true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list db roles: %w", err)
	}
	out := make(RolesList, len(roles))
	for i, role := range roles {
		out[i], err = dbRoleToAPIRole(&role)
		if err != nil {
			return nil, fmt.Errorf("convert db role: %w", err)
		}
	}
	return out, nil
}

// ListUserRoles returns a list of all roles for a user.
func (r *rbac) ListUserRoles(ctx context.Context, user string) (RolesList, error) {
	roles, err := raftdb.New(r.store.ReadDB()).ListBoundRolesForUser(ctx, raftdb.ListBoundRolesForUserParams{
		UserNames: sql.NullString{
			String: user,
			Valid:  true,
		},
		Users: sql.NullString{
			String: user,
			Valid:  true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list db roles: %w", err)
	}
	out := make(RolesList, len(roles))
	for i, role := range roles {
		out[i], err = dbRoleToAPIRole(&role)
		if err != nil {
			return nil, fmt.Errorf("convert db role: %w", err)
		}
	}
	return out, nil
}

func dbRoleToAPIRole(dbRole *raftdb.Role) (*v1.Role, error) {
	out := &v1.Role{
		Name:  dbRole.Name,
		Rules: []*v1.Rule{},
	}
	err := json.Unmarshal([]byte(dbRole.RulesJson), &out.Rules)
	if err != nil {
		return nil, fmt.Errorf("unmarshal rules: %w", err)
	}
	return out, nil
}

func dbRoleBindingToAPIRoleBinding(dbRoleBinding *raftdb.RoleBinding) *v1.RoleBinding {
	out := &v1.RoleBinding{
		Name:     dbRoleBinding.Name,
		Role:     dbRoleBinding.RoleName,
		Subjects: make([]*v1.Subject, 0),
	}
	if dbRoleBinding.UserNames.Valid {
		for _, user := range strings.Split(dbRoleBinding.UserNames.String, ",") {
			out.Subjects = append(out.Subjects, &v1.Subject{
				Type: v1.SubjectType_SUBJECT_USER,
				Name: user,
			})
		}
	}
	if dbRoleBinding.GroupNames.Valid {
		for _, group := range strings.Split(dbRoleBinding.GroupNames.String, ",") {
			out.Subjects = append(out.Subjects, &v1.Subject{
				Type: v1.SubjectType_SUBJECT_GROUP,
				Name: group,
			})
		}
	}
	if dbRoleBinding.NodeIds.Valid {
		for _, node := range strings.Split(dbRoleBinding.NodeIds.String, ",") {
			out.Subjects = append(out.Subjects, &v1.Subject{
				Type: v1.SubjectType_SUBJECT_NODE,
				Name: node,
			})
		}
	}
	// TODO: This should check for the "all" case and squash down to a single subject.
	return out
}