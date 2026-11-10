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
	"errors"
)

// ErrRewindUnsupported reports that a service or the requested session data
// cannot be rewound without losing information.
var ErrRewindUnsupported = errors.New("session rewind is not supported")

// ErrInvocationNotFound reports that a rewind target is absent from the active
// history, including an invocation already discarded by an earlier rewind.
// It also reports an attempt to append to a discarded invocation ID.
var ErrInvocationNotFound = errors.New("active invocation not found")

// ErrSessionRewound reports an append through a session handle obtained before
// a successful rewind. Fetch a fresh session before starting another invocation.
var ErrSessionRewound = errors.New("session was rewound")

// RewindService is an optional capability of a [Service]. Implementations restore
// session-scoped state and append a rewind marker atomically. They keep the raw
// event history, leave app/user state unchanged, and reject stale writes from
// before the rewind. Ordinary Service implementations need not implement it.
// Invocation IDs removed entirely from active history cannot be reused.
//
// InMemoryService supports this capability. It keeps a private state checkpoint
// before the first event of each invocation. Checkpoints support scalar values,
// map[string]any, []any, and []byte, including nested values. Other state types,
// and rewinds discarding artifact changes, return ErrRewindUnsupported.
type RewindService interface {
	Rewind(context.Context, *RewindRequest) error
}

// RewindRequest selects the state immediately before an invocation's first
// event. BeforeInvocationID must name an invocation in the active history.
type RewindRequest struct {
	AppName, UserID, SessionID string
	BeforeInvocationID         string
}

// ActiveEvents returns the history remaining after applying rewind markers,
// excluding the markers themselves. It leaves the input and stored events intact.
// Pass the complete history: filtering by time or event count first can omit a
// marker or its target and cannot reliably determine which events are active.
//
// Events remain in their original order and share their event pointers with the
// input. Callers must not modify them. A nil input produces an empty event list.
func ActiveEvents(history Events) Events {
	if history == nil {
		return events(nil)
	}
	first := make(map[string]int)
	hasRewind := false
	for i := 0; i < history.Len(); i++ {
		ev := history.At(i)
		if ev == nil {
			continue
		}
		if _, ok := first[ev.InvocationID]; !ok && ev.InvocationID != "" {
			first[ev.InvocationID] = i
		}
		hasRewind = hasRewind || ev.Actions.RewindBeforeInvocationID != ""
	}
	if !hasRewind {
		return history
	}
	var reversed events
	for i := history.Len() - 1; i >= 0; i-- {
		ev := history.At(i)
		if ev == nil {
			continue
		}
		if target := ev.Actions.RewindBeforeInvocationID; target != "" {
			if index, ok := first[target]; ok && index < i {
				i = index
			}
			continue
		}
		reversed = append(reversed, ev)
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed
}
