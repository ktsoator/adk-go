// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package session

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/platform"
)

type rewindCheckpoint struct {
	state     map[string]any
	supported bool
}

func (s *inMemoryService) Rewind(ctx context.Context, req *RewindRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req == nil || req.AppName == "" || req.UserID == "" || req.SessionID == "" || req.BeforeInvocationID == "" {
		return fmt.Errorf("rewind requires app, user, session, and invocation IDs")
	}
	ev := NewEvent(ctx, platform.NewUUID(ctx))
	ev.Author = "user"
	ev.Actions.RewindBeforeInvocationID = req.BeforeInvocationID
	key := id{appName: req.AppName, userID: req.UserID, sessionID: req.SessionID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, ok := s.sessions.Get(key.Encode())
	if !ok {
		return ErrNotFound
	}
	active := ActiveEvents(events(stored.events))
	index := -1
	for i := 0; i < active.Len(); i++ {
		if active.At(i).InvocationID == req.BeforeInvocationID {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrInvocationNotFound
	}
	for i := index; i < active.Len(); i++ {
		if len(active.At(i).Actions.ArtifactDelta) != 0 {
			return fmt.Errorf("%w: artifact restoration is not implemented", ErrRewindUnsupported)
		}
	}
	checkpoint, ok := stored.rewindCheckpoints[req.BeforeInvocationID]
	if !ok || !checkpoint.supported {
		return fmt.Errorf("%w: invocation has no supported state checkpoint", ErrRewindUnsupported)
	}
	// Unlike Python's compensating None values, restoring the whole local
	// checkpoint distinguishes absent keys from keys explicitly set to nil.
	// Copy again so callers of Get cannot mutate the private checkpoint.
	restored, _ := snapshotRewindState(checkpoint.state)
	if !ev.Timestamp.After(stored.updatedAt) {
		ev.Timestamp = stored.updatedAt.Add(time.Nanosecond)
	}
	stored.state = restored
	retainedInvocations := make(map[string]bool)
	for i := 0; i < index; i++ {
		retainedInvocations[active.At(i).InvocationID] = true
	}
	for i := index; i < active.Len(); i++ {
		invocationID := active.At(i).InvocationID
		if invocationID != "" && !retainedInvocations[invocationID] {
			stored.rewoundInvocations[invocationID] = struct{}{}
			delete(stored.rewindCheckpoints, invocationID)
		}
	}
	stored.events = append(stored.events, ev)
	stored.updatedAt = ev.Timestamp
	stored.rewindGeneration++
	return nil
}

func snapshotRewindState(state map[string]any) (map[string]any, bool) {
	out := make(map[string]any)
	for key, value := range state {
		if strings.HasPrefix(key, KeyPrefixApp) || strings.HasPrefix(key, KeyPrefixUser) || strings.HasPrefix(key, KeyPrefixTemp) {
			continue
		}
		copy, ok := snapshotRewindValue(value, 0)
		if !ok {
			return nil, false
		}
		out[key] = copy
	}
	return out, true
}

// Bound recursion for cyclic or unusually deep application state. Unsupported
// checkpoints disable rewind for that invocation, not normal session writes.
func snapshotRewindValue(value any, depth int) (any, bool) {
	if depth > 64 {
		return nil, false
	}
	switch value := value.(type) {
	case nil:
		return nil, true
	case map[string]any:
		if value == nil {
			return value, true
		}
		out := make(map[string]any, len(value))
		for key, item := range value {
			copy, ok := snapshotRewindValue(item, depth+1)
			if !ok {
				return nil, false
			}
			out[key] = copy
		}
		return out, true
	case []any:
		if value == nil {
			return value, true
		}
		out := make([]any, len(value))
		for i, item := range value {
			copy, ok := snapshotRewindValue(item, depth+1)
			if !ok {
				return nil, false
			}
			out[i] = copy
		}
		return out, true
	case []byte:
		return slices.Clone(value), true
	default:
		switch reflect.TypeOf(value).Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			return value, true
		default:
			return nil, false
		}
	}
}

var _ RewindService = (*inMemoryService)(nil)
