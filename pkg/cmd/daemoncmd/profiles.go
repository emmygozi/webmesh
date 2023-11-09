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

package daemoncmd

import (
	"bytes"

	v1 "github.com/webmeshproj/api/go/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ProfileID is a profile ID.
type ProfileID string

// ProfileIDs is a list of profile IDs.
type ProfileIDs []ProfileID

// Strings returns the string representations of the profile IDs.
func (ids ProfileIDs) Strings() []string {
	strs := make([]string, 0, len(ids))
	for _, id := range ids {
		strs = append(strs, id.String())
	}
	return strs
}

// String returns the string representation of the profile ID.
func (id ProfileID) String() string {
	return string(id)
}

// Bytes returns the byte representation of the profile ID.
func (id ProfileID) Bytes() []byte {
	return []byte(id)
}

var profilesPrefix = []byte("profiles")

// StorageKey returns the storage key for the profile ID.
func (id ProfileID) StorageKey() []byte {
	return bytes.Join([][]byte{profilesPrefix, id.Bytes()}, []byte("/"))
}

// ProfileIDFromKey returns the profile ID from the storage key.
func ProfileIDFromKey(key []byte) ProfileID {
	return ProfileID(bytes.TrimPrefix(key, append(profilesPrefix, '/')))
}

// Profiles is a map of profile IDs to connection parameters.
type Profiles map[ProfileID]Profile

// IDs returns the IDs of the profiles.
func (p Profiles) IDs() []ProfileID {
	ids := make([]ProfileID, 0, len(p))
	for id := range p {
		ids = append(ids, id)
	}
	return ids
}

// Profile contains the details of a connection profile
type Profile struct {
	*v1.ConnectionParameters
}

// MarshalJSON marshals the profile to JSON.
func (p Profile) MarshalJSON() ([]byte, error) {
	return protojson.Marshal(p.ConnectionParameters)
}

// UnmarshalJSON unmarshals the profile from JSON.
func (p *Profile) UnmarshalJSON(data []byte) error {
	if p.ConnectionParameters == nil {
		p.ConnectionParameters = &v1.ConnectionParameters{}
	}
	return protojson.Unmarshal(data, p.ConnectionParameters)
}

// MarshalProto marshals the profile to proto.
func (p Profile) MarshalProto() ([]byte, error) {
	return proto.Marshal(p.ConnectionParameters)
}

// UnmarshalProto unmarshals the profile from proto.
func (p *Profile) UnmarshalProto(data []byte) error {
	if p.ConnectionParameters == nil {
		p.ConnectionParameters = &v1.ConnectionParameters{}
	}
	return proto.Unmarshal(data, p.ConnectionParameters)
}
