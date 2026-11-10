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

package remoteagent

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"google.golang.org/adk/v2/runner"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
)

func TestNewMessageRewindHistory(t *testing.T) {
	svc := session.InMemoryService()
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"retained", "discarded"} {
		ev := newEventFromParts("user", genai.NewPartFromText(text))
		ev.InvocationID = text
		if err := svc.AppendEvent(t.Context(), created.Session, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.(session.RewindService).Rewind(t.Context(), &session.RewindRequest{AppName: "app", UserID: "u", SessionID: "s", BeforeInvocationID: "discarded"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(agent.Config{Name: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{Agent: a, Session: fresh.Session})
	msg, err := newMessage(ctx, A2AConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text() != "retained" {
		t.Fatal("outbound message includes discarded history")
	}
}

func TestNewMessageRewindRejectsRemoteContextReuse(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "conversation", true: "pending task"}[resume], func(t *testing.T) {
			svc := session.InMemoryService()
			created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u", SessionID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			a := newEventFromParts("remote", genai.NewPartFromText("reply"))
			a.InvocationID = "A"
			a.CustomMetadata = adka2a.ToCustomMetadata("task", "context")
			if resume {
				a.Content = genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "approve"}}}, genai.RoleModel)
				a.CustomMetadata = adka2a.ToCustomMetadata("task", "")
			}
			b := newEventFromParts("user", genai.NewPartFromText("discarded"))
			b.InvocationID = "B"
			for _, ev := range []*session.Event{a, b} {
				if err := svc.AppendEvent(t.Context(), created.Session, ev); err != nil {
					t.Fatal(err)
				}
			}
			req := &session.RewindRequest{AppName: "app", UserID: "u", SessionID: "s", BeforeInvocationID: "B"}
			if err := svc.(session.RewindService).Rewind(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			get := func() session.Session {
				t.Helper()
				res, err := svc.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: "s"})
				if err != nil {
					t.Fatal(err)
				}
				return res.Session
			}
			fresh := get()
			next := newEventFromParts("user", genai.NewPartFromText("next"))
			next.InvocationID = "C"
			if resume {
				next.Content = genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "approve"}}}, genai.RoleUser)
			}
			if err := svc.AppendEvent(t.Context(), fresh, next); err != nil {
				t.Fatal(err)
			}
			ag, err := agent.New(agent.Config{Name: "remote"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{Agent: ag, Session: fresh})
			if _, err := newMessage(ctx, A2AConfig{}); !errors.Is(err, session.ErrRewindUnsupported) {
				t.Fatal("reused remote state across a local rewind")
			}
			req.BeforeInvocationID = "A"
			if err := svc.(session.RewindService).Rewind(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			ctx = icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{Agent: ag, Session: get()})
			msg, err := newMessage(ctx, A2AConfig{})
			if err != nil || msg.ContextID != "" || msg.TaskID != "" {
				t.Fatal("fresh remote context was rejected")
			}
		})
	}
}

func TestRewindAllowsNewRemoteContext(t *testing.T) {
	for _, tc := range []struct{ name, author, scope, task, context string }{
		{"new IDs", "remote", "", "old-task", "old-context"},
		{"another peer", "other", "", "new-task", "new-context"},
		{"another scope", "remote", "other-scope", "new-task", "new-context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &session.Event{Author: tc.author, IsolationScope: tc.scope, LLMResponse: model.LLMResponse{CustomMetadata: adka2a.ToCustomMetadata(a2a.TaskID(tc.task), tc.context)}}
			marker := &session.Event{Actions: session.EventActions{RewindBeforeInvocationID: "old"}}
			fresh := &session.Event{Author: "remote", LLMResponse: model.LLMResponse{CustomMetadata: adka2a.ToCustomMetadata("new-task", "new-context")}}
			base := newTestInvocationContext(t, "remote")
			ctx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{Agent: base.Agent(), Session: rewindHistorySession{Session: base.Session(), events: rewindEvents{old, marker, fresh}}})
			if err := checkRewoundRemoteContext(ctx, "new-task", "new-context"); err != nil {
				t.Fatal("fresh context was confused with a pre-rewind remote context")
			}
		})
	}
}

type rewindHistorySession struct {
	session.Session
	events session.Events
}

func (s rewindHistorySession) Events() session.Events { return s.events }

type rewindEvents []*session.Event

func (e rewindEvents) Len() int                { return len(e) }
func (e rewindEvents) At(i int) *session.Event { return e[i] }
func (e rewindEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}

func TestRewindRemoteRejectionDoesNotBecomeHistoryBoundary(t *testing.T) {
	client := &beforeRunClient{contextID: "remote-context"}
	remote, err := NewA2A(A2AConfig{Name: "remote", AgentCard: &a2a.AgentCard{}, ClientProvider: func(context.Context, *a2a.AgentCard) (A2AClient, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "app", Agent: remote, SessionService: svc, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for range 2 {
		for ev, err := range r.Run(t.Context(), "u", "s", genai.NewContentFromText("turn", genai.RoleUser), agent.RunConfig{}) {
			if err != nil {
				t.Fatal(err)
			}
			target = ev.InvocationID
		}
	}
	if err := r.Rewind(t.Context(), "u", "s", target); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rejected := false
		for ev, err := range r.Run(t.Context(), "u", "s", genai.NewContentFromText("retry", genai.RoleUser), agent.RunConfig{}) {
			if err != nil {
				if !errors.Is(err, session.ErrRewindUnsupported) {
					t.Fatal("wrong remote-reuse error")
				}
				rejected = true
				continue
			}
			if ev.Author == "remote" {
				t.Fatal("local rejection became a remote event")
			}
		}
		if !rejected {
			t.Fatal("remote context guard was bypassed on retry")
		}
	}
	if client.calls != 2 {
		t.Fatal("a rejected remote request reached the peer")
	}
}
