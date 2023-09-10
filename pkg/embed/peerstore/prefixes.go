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

package peerstore

import (
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// StorageKey is a key prefix for values in the database.
type StorageKey string

// Prefixes used in the database. All get prepended with the
// peer ID to produce paths such as /<peer ID>/private-key.
const (
	// PrivateKeys is the key prefix for private keys.
	PrivateKeys StorageKey = "/private-key"
	// PublicKeys is the key prefix for public keys.
	PublicKeys StorageKey = "/public-key"
	// Multiaddrs is the key prefix for multiaddrs.
	Multiaddrs StorageKey = "/multiaddrs"
	// Protocols is the key prefix for protocols.
	Protocols StorageKey = "/protocols"
	// Observations is the key prefix for observations.
	Observations StorageKey = "/observations"
)

// Key returns this key as a byte slice.
func (p StorageKey) Key() []byte { return []byte(p) }

// Key returns this key as a string.
func (p StorageKey) String() string { return string(p) }

// PathFor computes the path for this key based on the given peer ID.
func (p StorageKey) PathFor(peer peer.ID) StorageKey {
	return StorageKey("/" + peer.String() + p.String())
}

// KeyFor computes the key for this key based on the given value.
func (p StorageKey) KeyFor(value string) StorageKey {
	return StorageKey(p.String() + "/" + value)
}

// Trim strips the prefix from the given key.
func (p StorageKey) Trim(key []byte) []byte {
	len := len(p)
	if strings.Contains(string(p), string(Multiaddrs)) || strings.Contains(string(p), string(Protocols)) {
		// We need to trim the leading slash.
		len++
	}
	return key[len:]
}
