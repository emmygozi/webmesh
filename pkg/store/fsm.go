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

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang/snappy"
	"github.com/hashicorp/raft"
	v1 "gitlab.com/webmesh/api/v1"
	"golang.org/x/exp/slog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"gitlab.com/webmesh/node/pkg/meshdb/models/localdb"
)

// ApplyBatch implements the raft.BatchingFSM interface.
func (s *store) ApplyBatch(logs []*raft.Log) []any {
	s.log.Debug("applying batch", slog.Int("count", len(logs)))
	res := make([]any, len(logs))
	for i, l := range logs {
		res[i] = s.Apply(l)
	}
	return res
}

// Apply applies a Raft log entry to the store.
func (s *store) Apply(l *raft.Log) any {
	log := s.log.With(slog.Int("index", int(l.Index)), slog.Int("term", int(l.Term)))
	log.Debug("applying log")
	start := time.Now()
	defer func() {
		log.Debug("finished applying log", slog.String("took", time.Since(start).String()))
	}()
	defer s.raftIndex.Store(l.Index)

	// Validate and store the term/index to the local DB

	dbTerm, dbIndex, err := s.getDBTermAndIndex()
	if err != nil {
		// This is a fatal error. We need to check the term and index of the
		// log entry against the database, but if we can't do that, we can't
		// apply the log entry.
		// TODO: This should trigger some sort of recovery.
		log.Error("error checking last applied index", slog.String("error", err.Error()))
		return &v1.RaftApplyResponse{
			Time:  time.Since(start).String(),
			Error: fmt.Sprintf("check last applied index: %s", err.Error()),
		}
	}
	log.Debug("last applied index",
		slog.Int("last-term", int(dbTerm)),
		slog.Int("last-index", int(dbIndex)))

	if l.Term < dbTerm {
		log.Debug("received log from old term")
		return &v1.RaftApplyResponse{
			Time: time.Since(start).String(),
		}
	} else if l.Index <= dbIndex {
		log.Debug("log already applied to database")
		return &v1.RaftApplyResponse{
			Time: time.Since(start).String(),
		}
	}

	// Save the new index to the db when we are done
	defer func() {
		q := localdb.New(s.LocalDB())
		if dbIndex != l.Index {
			log.Debug("updating last applied index in db")
			err := q.SetCurrentRaftIndex(context.Background(), localdb.SetCurrentRaftIndexParams{
				LogIndex: int64(l.Index),
				Term:     int64(l.Term),
			})
			if err != nil {
				// We'll live. This isn't a fatal error, but it's not great.
				// Next boot will just have to replay the log entries and might
				// have some local constraint errors.
				log.Error("error updating last applied index", slog.String("error", err.Error()))
			}
		}
	}()

	if l.Type != raft.LogCommand {
		log.Debug("not a data query log", slog.String("type", l.Type.String()))
		return &v1.RaftApplyResponse{
			Time: time.Since(start).String(),
		}
	}

	// Decode the log entry
	var cmd v1.RaftLogEntry
	switch s.raftLogFormat {
	case RaftLogFormatJSON:
		err = protojson.Unmarshal(l.Data, &cmd)
	case RaftLogFormatProtobuf:
		err = proto.Unmarshal(l.Data, &cmd)
	case RaftLogFormatProtobufSnappy:
		var decoded []byte
		decoded, err = snappy.Decode(nil, l.Data)
		if err == nil {
			err = proto.Unmarshal(decoded, &cmd)
		}
	default:
		err = fmt.Errorf("unknown raft log format: %s", s.raftLogFormat)
	}
	if err != nil {
		// This is a fatal error. We can't apply the log entry if we can't
		// decode it. This should never happen.
		log.Error("error decoding raft log entry", slog.String("error", err.Error()))
		return &v1.RaftApplyResponse{
			Time:  time.Since(start).String(),
			Error: fmt.Sprintf("unmarshal raft log entry: %s", err.Error()),
		}
	}

	return s.apply(l, &cmd, log, start)
}

// Snapshot returns a Raft snapshot.
func (s *store) Snapshot() (raft.FSMSnapshot, error) {
	s.dataMux.Lock()
	defer s.dataMux.Unlock()
	return s.snapshotter.Snapshot(context.Background())
}

// Restore restores a Raft snapshot.
func (s *store) Restore(r io.ReadCloser) error {
	s.dataMux.Lock()
	defer s.dataMux.Unlock()
	return s.snapshotter.Restore(context.Background(), r)
}

func (s *store) getDBTermAndIndex() (term, index uint64, err error) {
	// Check if the current term and index is already applied.
	raftState, err := localdb.New(s.LocalDB()).GetCurrentRaftIndex(context.Background())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No rows means we haven't applied any log entries yet.
			return 0, 0, nil
		}
		err = fmt.Errorf("get raft state: %w", err)
		return
	}
	return uint64(raftState.Term), uint64(raftState.LogIndex), nil
}
