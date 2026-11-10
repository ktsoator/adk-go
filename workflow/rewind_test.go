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

package workflow

import (
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

func TestRewindExcludesWorkflowHistory(t *testing.T) {
	wf, err := New("rewind", []Edge{{From: Start, To: newDummyNode("ask")}})
	if err != nil {
		t.Fatal(err)
	}
	ev := &session.Event{InvocationID: "test-invocation-id", Author: "ask", LongRunningToolIDs: []string{"call"}, Output: "discarded", NodeInfo: &session.NodeInfo{Path: "wf/ask@1"}}
	raw := sliceEvents{ev}
	state, err := wf.ReconstructRunState(fakeSession{events: raw}, "test-invocation-id")
	if err != nil || state == nil {
		t.Fatal("fixture did not reconstruct a pending run")
	}
	raw = append(raw, &session.Event{Actions: session.EventActions{RewindBeforeInvocationID: "test-invocation-id"}})
	state, err = wf.ReconstructRunState(fakeSession{events: raw}, "test-invocation-id")
	if err != nil || state != nil {
		t.Fatal("rewound workflow was reconstructed")
	}
	// A completed child is cacheable before rewind; the earlier fixture's
	// long-running call intentionally was not a completed result.
	ev.LongRunningToolIDs = nil
	ctx := newMockCtx(t)
	ctx.sess = &eventsSession{events: raw[:1]}
	before := newDynamicSubScheduler(agent.NewContext(ctx), "wf", noopEmit).(*dynamicSubScheduler)
	if _, hit := before.awaitOrLead("wf/ask@1"); !hit {
		t.Fatal("fixture did not cache a completed output")
	}
	ctx.sess = &eventsSession{events: raw}
	sub := newDynamicSubScheduler(agent.NewContext(ctx), "wf", noopEmit).(*dynamicSubScheduler)
	if _, hit := sub.awaitOrLead("wf/ask@1"); hit {
		t.Fatal("rewound output was reused from cache")
	}
}
