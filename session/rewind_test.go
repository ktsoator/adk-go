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
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/platform"
)

func TestActiveEvents(t *testing.T) {
	event := func(inv string) *Event { return &Event{InvocationID: inv} }
	marker := func(target string) *Event { return &Event{Actions: EventActions{RewindBeforeInvocationID: target}} }
	for _, tc := range []struct {
		name string
		raw  events
		want []string
	}{
		{name: "empty"},
		{name: "ordinary", raw: events{event("A"), event("B")}, want: []string{"A", "B"}},
		{name: "whole invocation", raw: events{event("A"), event("B"), event("B"), marker("B"), event("C")}, want: []string{"A", "C"}},
		{name: "repeated", raw: events{event("A"), event("B"), marker("B"), event("C"), marker("C"), event("D")}, want: []string{"A", "D"}},
		{name: "cross earlier rewind", raw: events{event("A"), event("B"), marker("B"), event("C"), marker("A"), event("D")}, want: []string{"D"}},
		{name: "same invocation resumed", raw: events{event("A"), event("B"), event("A"), marker("A")}},
		{name: "unknown marker", raw: events{event("A"), marker("absent")}, want: []string{"A"}},
		{name: "future target", raw: events{event("A"), marker("B"), event("B")}, want: []string{"A", "B"}},
		{name: "nil entries", raw: events{nil, event("A"), marker("A"), nil, event("B")}, want: []string{"B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.raw)
			active := ActiveEvents(tc.raw)
			var got []string
			for ev := range active.All() {
				got = append(got, ev.InvocationID)
			}
			if !slices.Equal(got, tc.want) {
				t.Error("wrong active invocation sequence")
			}
			if !slices.Equal(tc.raw, before) {
				t.Error("raw event sequence was mutated")
			}
		})
	}
	if ActiveEvents(nil).Len() != 0 {
		t.Fatal("nil history must be empty")
	}
}

func TestInMemoryRewindStateAndFencing(t *testing.T) {
	initialNested := map[string]any{"items": []any{"initial", []byte{1, 2}}}
	svc, handle, req := rewindTestSession(t, map[string]any{"nested": initialNested, "nullable": nil, "number": int64(7)})
	appendTurn := func(inv string, delta map[string]any) {
		t.Helper()
		ev := NewEvent(t.Context(), inv)
		ev.Actions.StateDelta = delta
		if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("A", map[string]any{"number": int64(8)})
	appendTurn("A", map[string]any{"answer": "first"})
	initialNested["items"].([]any)[0] = "changed"
	initialNested["items"].([]any)[1].([]byte)[0] = 99
	appendTurn("B", map[string]any{"number": int64(9), "introduced": true, "user:setting": true, "app:flag": true})
	appendTurn("B", map[string]any{"answer": "second"})
	request := &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "B"}
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	fresh := rewindTestGet(t, svc, req)
	if _, err := fresh.State().Get("introduced"); !errors.Is(err, ErrStateKeyNotExist) {
		t.Fatal("discarded key still exists")
	}
	if value, err := fresh.State().Get("nullable"); err != nil || value != nil {
		t.Fatal("nil was confused with absence")
	}
	if value, _ := fresh.State().Get("number"); value != int64(8) {
		t.Fatal("checkpoint value or type changed")
	}
	if value, _ := fresh.State().Get("answer"); value != "first" {
		t.Fatal("checkpoint was overwritten by a later event in the same invocation")
	}
	if fresh.Events().Len() != 5 || ActiveEvents(fresh.Events()).Len() != 2 {
		t.Fatal("raw or active history length is incorrect")
	}
	if fresh.Events().At(4).IsFinalResponse() {
		t.Fatal("rewind marker is a final response")
	}
	before := handle.Events().Len()
	if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), "late")); !errors.Is(err, ErrSessionRewound) {
		t.Fatal("old handle accepted a late write")
	}
	if handle.Events().Len() != before || rewindTestGet(t, svc, req).Events().Len() != 5 {
		t.Fatal("failed append mutated a session")
	}
	request.BeforeInvocationID = "A"
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	fresh = rewindTestGet(t, svc, req)
	want := map[string]any{
		"nested":   map[string]any{"items": []any{"initial", []byte{1, 2}}},
		"nullable": nil, "number": int64(7), "user:setting": true, "app:flag": true,
	}
	if !reflect.DeepEqual(maps.Collect(fresh.State().All()), want) {
		t.Fatal("initial checkpoint or shared state was not restored correctly")
	}
	listed, err := svc.List(t.Context(), &ListRequest{AppName: req.AppName, UserID: req.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(maps.Collect(listed.Sessions[0].State().All()), want) {
		t.Fatal("List sees a different stored state")
	}
	// A caller may edit its returned events, but cannot insert or change a
	// canonical marker by changing those scalar fields.
	fresh.Events().At(0).Actions.RewindBeforeInvocationID = "A"
	fresh.Events().At(5).Actions.RewindBeforeInvocationID = ""
	if ActiveEvents(rewindTestGet(t, svc, req).Events()).Len() != 0 {
		t.Fatal("caller changed a stored rewind marker")
	}
	encoded, err := json.Marshal(rewindTestGet(t, svc, req).Events().At(5))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Actions.RewindBeforeInvocationID != "A" {
		t.Fatal("rewind marker did not survive JSON")
	}
}

func TestInMemoryRewindFailuresDoNotMutate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    map[string]any
		artifact bool
		target   string
		want     error
	}{
		{name: "unknown", target: "missing", want: ErrInvocationNotFound},
		{name: "artifact", target: "A", artifact: true, want: ErrRewindUnsupported},
		{name: "unsupported checkpoint", state: map[string]any{"value": make(chan int)}, target: "A", want: ErrRewindUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, handle, req := rewindTestSession(t, tc.state)
			ev := NewEvent(t.Context(), "A")
			ev.Actions.StateDelta["new"] = true
			if tc.artifact {
				ev.Actions.ArtifactDelta["file"] = 1
			}
			if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
				t.Fatal(err)
			}
			err := svc.Rewind(t.Context(), &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: tc.target})
			if !errors.Is(err, tc.want) {
				t.Fatal("rewind returned the wrong error")
			}
			fresh := rewindTestGet(t, svc, req)
			if fresh.Events().Len() != 1 {
				t.Fatal("failed rewind appended an event")
			}
			if _, err := fresh.State().Get("new"); err != nil {
				t.Fatal("failed rewind modified state")
			}
			if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), "B")); err != nil {
				t.Fatal("failed rewind invalidated a handle")
			}
		})
	}
	svc, handle, req := rewindTestSession(t, nil)
	ev := NewEvent(t.Context(), "A")
	if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
		t.Fatal(err)
	}
	request := &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := svc.Rewind(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("rewind ignored cancellation")
	}
	for _, invalid := range []*RewindRequest{
		nil,
		{},
		{UserID: "user", SessionID: "session", BeforeInvocationID: "A"},
		{AppName: "app", SessionID: "session", BeforeInvocationID: "A"},
		{AppName: "app", UserID: "user", BeforeInvocationID: "A"},
		{AppName: "app", UserID: "user", SessionID: "session"},
	} {
		if err := svc.Rewind(t.Context(), invalid); err == nil {
			t.Fatal("accepted an incomplete rewind request")
		}
	}
	missing := *request
	missing.UserID = "another-user"
	if err := svc.Rewind(t.Context(), &missing); !errors.Is(err, ErrNotFound) {
		t.Fatal("rewind crossed user ownership")
	}
	forged := NewEvent(t.Context(), "forged")
	forged.Actions.RewindBeforeInvocationID = "A"
	if err := svc.AppendEvent(t.Context(), handle, forged); !errors.Is(err, ErrRewindUnsupported) {
		t.Fatal("AppendEvent accepted a forged rewind")
	}
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rewind(t.Context(), request); !errors.Is(err, ErrInvocationNotFound) {
		t.Fatal("accepted an already discarded invocation")
	}
}

func TestInMemoryRewindConcurrentAndTimestamp(t *testing.T) {
	svc, handle, req := rewindTestSession(t, nil)
	ev := NewEvent(t.Context(), "A")
	ev.Timestamp = time.Now().Add(time.Hour)
	if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			errorsCh <- svc.Rewind(t.Context(), &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"})
		})
	}
	wg.Wait()
	close(errorsCh)
	successes := 0
	for err := range errorsCh {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInvocationNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent rewinds did not commit exactly once")
	}
	fresh := rewindTestGet(t, svc, req)
	if fresh.Events().Len() != 2 || !fresh.Events().At(1).Timestamp.After(ev.Timestamp) {
		t.Fatal("rewind marker broke log ordering")
	}
}

func TestRewindSnapshotValues(t *testing.T) {
	var nilMap map[string]any
	var nilSlice []any
	type namedInt int
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for _, tc := range []struct {
		name      string
		value     any
		supported bool
	}{
		{"nil", nil, true},
		{"nil map", nilMap, true},
		{"nil slice", nilSlice, true},
		{"nested", map[string]any{"a": []any{nil, true, "text", namedInt(2), float64(1.5), []byte{1}}}, true},
		{"pointer", new(int), false},
		{"nested map unsupported", map[string]any{"bad": make(chan int)}, false},
		{"nested unsupported", []any{make(chan int)}, false},
		{"cycle", cyclic, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy, ok := snapshotRewindValue(tc.value, 0)
			if ok != tc.supported {
				t.Fatal("wrong checkpoint support result")
			}
			if ok && !reflect.DeepEqual(copy, tc.value) {
				t.Fatal("checkpoint changed a value or its type")
			}
		})
	}
	state, ok := snapshotRewindState(map[string]any{"app:x": make(chan int), "user:x": make(chan int), "temp:x": make(chan int), "x": nil})
	if !ok || len(state) != 1 {
		t.Fatal("non-session state entered a checkpoint")
	}
}

func TestInMemoryRewindProviderCanReadSession(t *testing.T) {
	svc, handle, req := rewindTestSession(t, nil)
	if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), "A")); err != nil {
		t.Fatal(err)
	}
	ctx := platform.WithUUIDProvider(t.Context(), func() string {
		if _, err := svc.Get(t.Context(), &req); err != nil {
			t.Error(err)
		}
		return "rewind-id"
	})
	done := make(chan error, 1)
	go func() {
		done <- svc.Rewind(ctx, &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rewind called a provider while holding the service lock")
	}
}

func rewindTestSession(t *testing.T, initial map[string]any) (*inMemoryService, Session, GetRequest) {
	t.Helper()
	svc := InMemoryService().(*inMemoryService)
	created, err := svc.Create(t.Context(), &CreateRequest{AppName: "app", UserID: "user", State: initial})
	if err != nil {
		t.Fatal(err)
	}
	return svc, created.Session, GetRequest{AppName: "app", UserID: "user", SessionID: created.Session.ID()}
}

func rewindTestGet(t *testing.T, svc Service, req GetRequest) Session {
	t.Helper()
	res, err := svc.Get(t.Context(), &req)
	if err != nil {
		t.Fatal(err)
	}
	return res.Session
}

func TestInMemoryRewindRejectsInvocationReuse(t *testing.T) {
	svc, handle, req := rewindTestSession(t, nil)
	for _, inv := range []string{"A", "B"} {
		if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), inv)); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Rewind(t.Context(), &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "B"}); err != nil {
		t.Fatal(err)
	}
	fresh := rewindTestGet(t, svc, req)
	if err := svc.AppendEvent(t.Context(), fresh, NewEvent(t.Context(), "C")); err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendEvent(t.Context(), fresh, NewEvent(t.Context(), "B")); !errors.Is(err, ErrInvocationNotFound) {
		t.Fatal("accepted reuse of a discarded invocation ID")
	}
	if fresh.Events().Len() != 4 || rewindTestGet(t, svc, req).Events().Len() != 4 {
		t.Fatal("rejected append changed history")
	}
}

func TestInMemoryRewindArtifactGuardOwnsItsMap(t *testing.T) {
	svc, handle, req := rewindTestSession(t, nil)
	ev := NewEvent(t.Context(), "A")
	ev.Actions.ArtifactDelta["file"] = 1
	if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
		t.Fatal(err)
	}
	clear(rewindTestGet(t, svc, req).Events().At(0).Actions.ArtifactDelta)
	if err := svc.Rewind(t.Context(), &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"}); !errors.Is(err, ErrRewindUnsupported) {
		t.Fatal("caller bypassed artifact guard through an aliased map")
	}
}

func TestInMemoryRewindRetainsResumablePrefix(t *testing.T) {
	svc, handle, req := rewindTestSession(t, map[string]any{"initial": true})
	for _, inv := range []string{"A", "B", "A"} {
		if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), inv)); err != nil {
			t.Fatal(err)
		}
	}
	request := &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "B"}
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	fresh := rewindTestGet(t, svc, req)
	if err := svc.AppendEvent(t.Context(), fresh, NewEvent(t.Context(), "A")); err != nil {
		t.Fatal("retained invocation cannot be resumed")
	}
	request.BeforeInvocationID = "A"
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal("retained invocation lost its checkpoint")
	}
	if ActiveEvents(rewindTestGet(t, svc, req).Events()).Len() != 0 {
		t.Fatal("resumed invocation was not fully discarded")
	}
}

func TestInMemoryRewindCancellationAroundProviders(t *testing.T) {
	for _, early := range []bool{true, false} {
		t.Run(map[bool]string{true: "before provider", false: "during provider"}[early], func(t *testing.T) {
			svc, handle, req := rewindTestSession(t, nil)
			if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), "A")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			ctx = platform.WithUUIDProvider(ctx, func() string { calls++; cancel(); return "marker" })
			if early {
				cancel()
			}
			if err := svc.Rewind(ctx, &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"}); !errors.Is(err, context.Canceled) {
				t.Fatal("rewind ignored cancellation")
			}
			if early && calls != 0 {
				t.Fatal("canceled rewind called a provider")
			}
			if rewindTestGet(t, svc, req).Events().Len() != 1 {
				t.Fatal("canceled rewind changed history")
			}
		})
	}
}

func TestInMemoryRewindRacesAppend(t *testing.T) {
	for range 32 {
		svc, handle, req := rewindTestSession(t, map[string]any{"value": "initial"})
		ev := NewEvent(t.Context(), "A")
		ev.Actions.StateDelta["value"] = "changed"
		if err := svc.AppendEvent(t.Context(), handle, ev); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		appended := make(chan error, 1)
		go func() {
			<-start
			late := NewEvent(t.Context(), "B")
			late.Actions.StateDelta["value"] = "late"
			appended <- svc.AppendEvent(t.Context(), handle, late)
		}()
		close(start)
		if err := svc.Rewind(t.Context(), &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "A"}); err != nil {
			t.Fatal(err)
		}
		err := <-appended
		if err != nil && !errors.Is(err, ErrSessionRewound) {
			t.Fatal("append failed outside the rewind fence")
		}
		fresh := rewindTestGet(t, svc, req)
		if value, _ := fresh.State().Get("value"); value != "initial" {
			t.Fatal("concurrent append survived state restoration")
		}
		if ActiveEvents(fresh.Events()).Len() != 0 {
			t.Fatal("concurrent append survived history restoration")
		}
	}
}

func TestInMemoryRewindRestoredStateDoesNotAliasCheckpoint(t *testing.T) {
	svc, handle, req := rewindTestSession(t, map[string]any{"nested": map[string]any{"items": []any{[]byte{1}}}})
	for _, inv := range []string{"A", "B"} {
		if err := svc.AppendEvent(t.Context(), handle, NewEvent(t.Context(), inv)); err != nil {
			t.Fatal(err)
		}
	}
	request := &RewindRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID, BeforeInvocationID: "B"}
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	fresh := rewindTestGet(t, svc, req)
	nested, err := fresh.State().Get("nested")
	if err != nil {
		t.Fatal(err)
	}
	nested.(map[string]any)["items"].([]any)[0].([]byte)[0] = 99
	request.BeforeInvocationID = "A"
	if err := svc.Rewind(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	restored, err := rewindTestGet(t, svc, req).State().Get("nested")
	if err != nil {
		t.Fatal(err)
	}
	if restored.(map[string]any)["items"].([]any)[0].([]byte)[0] != 1 {
		t.Fatal("restored state corrupted a retained checkpoint")
	}
}
