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

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"
)

func TestRunnerRewind(t *testing.T) {
	ctx := t.Context()
	initial := map[string]any{"city": "Shanghai", "language": "zh", "nullable": nil}
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "rewind", UserID: "user", State: initial})
	if err != nil {
		t.Fatal(err)
	}
	req := session.GetRequest{AppName: "rewind", UserID: "user", SessionID: created.Session.ID()}
	get := func() session.Session {
		t.Helper()
		res, err := svc.Get(ctx, &req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Session
	}
	llm := &rewindModel{t: t, want: [][]string{
		{"turn A"},
		{"turn A", "reply A", "turn B"},
		{"turn A", "reply A", "turn C"},
		{"turn A", "reply A", "turn D"},
		{"turn E"},
	}}
	var beforeModel []map[string]any
	a, err := llmagent.New(llmagent.Config{
		Name: "assistant", Model: llm, OutputKey: "answer",
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
			beforeModel = append(beforeModel, maps.Collect(ctx.State().All()))
			return nil, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{AppName: req.AppName, Agent: a, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	run := func(turn string, delta map[string]any) string {
		t.Helper()
		var invocationID string
		var finalResponses int
		for ev, err := range r.Run(ctx, req.UserID, req.SessionID,
			genai.NewContentFromText("turn "+turn, genai.RoleUser), agent.RunConfig{}, WithStateDelta(delta)) {
			if err != nil {
				t.Fatal(err)
			}
			invocationID = ev.InvocationID
			if ev.IsFinalResponse() {
				finalResponses++
			}
		}
		if invocationID == "" || finalResponses != 1 {
			t.Fatalf("run produced %d final responses; invocation present: %t", finalResponses, invocationID != "")
		}
		return invocationID
	}
	invA := run("A", map[string]any{"city": "Hangzhou"})
	invB := run("B", map[string]any{"city": "Beijing", "booking": true, "user:timezone": "UTC+8", "app:enabled": true})
	rawBefore := get()
	originalEvents := encodeRewindEvents(t, slices.Collect(rawBefore.Events().All()))
	if err := r.Rewind(ctx, req.UserID, req.SessionID, invB); err != nil {
		t.Fatal(err)
	}
	stateAfterA := map[string]any{
		"city": "Hangzhou", "language": "zh", "nullable": nil, "answer": "reply A",
		"user:timezone": "UTC+8", "app:enabled": true,
	}
	view := get()
	assertRewindState(t, maps.Collect(view.State().All()), stateAfterA)
	if value, err := view.State().Get("nullable"); err != nil || value != nil {
		t.Fatal("an explicitly nil state value must remain present")
	}
	assertRewindMissing(t, view.State(), "booking")
	if view.ID() != req.SessionID || session.ActiveEvents(view.Events()).Len() != 2 {
		t.Fatal("rewind changed session identity or retained the wrong history")
	}
	if got := get().Events().Len(); got != 5 {
		t.Fatalf("raw log has %d events, want four original events and a rewind marker", got)
	}
	invC := run("C", map[string]any{"city": "Shenzhen", "branch_only": true})
	if err := r.Rewind(ctx, req.UserID, req.SessionID, invC); err != nil {
		t.Fatal(err)
	}
	assertRewindState(t, maps.Collect(get().State().All()), stateAfterA)
	assertRewindMissing(t, get().State(), "branch_only")
	run("D", nil)
	if err := r.Rewind(ctx, req.UserID, req.SessionID, invA); err != nil {
		t.Fatal(err)
	}
	wantInitial := map[string]any{
		"city": "Shanghai", "language": "zh", "nullable": nil,
		"user:timezone": "UTC+8", "app:enabled": true,
	}
	view = get()
	assertRewindState(t, maps.Collect(view.State().All()), wantInitial)
	assertRewindMissing(t, view.State(), "answer")
	if session.ActiveEvents(view.Events()).Len() != 0 {
		t.Fatal("rewind to the first invocation left conversation history")
	}
	run("E", nil)
	if llm.calls != 5 || len(beforeModel) != 5 {
		t.Fatal("not all scripted model requests were exercised")
	}
	assertRewindState(t, beforeModel[3], stateAfterA)
	assertRewindState(t, beforeModel[4], wantInitial)
	for _, index := range []int{2, 3, 4} {
		if _, exists := beforeModel[index]["booking"]; exists {
			t.Error("discarded state reached a model callback")
		}
	}
	raw := get()
	if raw.Events().Len() != 13 {
		t.Fatalf("raw log has %d events, want ten conversation events and three rewind markers", raw.Events().Len())
	}
	if !bytes.Equal(originalEvents, encodeRewindEvents(t, []*session.Event{
		raw.Events().At(0), raw.Events().At(1), raw.Events().At(2), raw.Events().At(3),
	})) {
		t.Fatal("rewind modified original stored events")
	}
	assertRewindMissing(t, raw.State(), "booking")
	listed, err := svc.List(ctx, &session.ListRequest{AppName: req.AppName, UserID: req.UserID})
	if err != nil {
		t.Fatal(err)
	}
	assertRewindMissing(t, listed.Sessions[0].State(), "booking")
	t.Log("five real Runner turns, three rewinds: prompts and callback state matched; all 13 raw events retained")
}

type rewindModel struct {
	t     *testing.T
	want  [][]string
	calls int
}

func (m *rewindModel) Name() string { return "rewind-script" }

func (m *rewindModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.calls >= len(m.want) {
			m.t.Fatal("unexpected extra model request")
		}
		var contents []string
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				contents = append(contents, part.Text)
			}
		}
		if !reflect.DeepEqual(contents, m.want[m.calls]) {
			m.t.Errorf("model request %d did not match the expected active conversation", m.calls+1)
		}
		response := "reply " + string(rune('A'+m.calls))
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(response, genai.RoleModel), TurnComplete: true}, nil)
	}
}

func assertRewindState(t *testing.T, state, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(state, want) {
		t.Error("state does not match the expected checkpoint")
	}
}

func assertRewindMissing(t *testing.T, state session.State, key string) {
	t.Helper()
	if _, err := state.Get(key); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Error("key introduced by a discarded turn is still present")
	}
}

func encodeRewindEvents(t *testing.T, events []*session.Event) []byte {
	t.Helper()
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRunnerRewindUnsupportedAndCanceled(t *testing.T) {
	// Embedding only Service deliberately hides the optional capability.
	r := &Runner{sessionService: struct{ session.Service }{session.InMemoryService()}}
	if err := r.Rewind(t.Context(), "u", "s", "A"); !errors.Is(err, session.ErrRewindUnsupported) {
		t.Fatal("a service without rewind capability was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Rewind(ctx, "u", "s", "A"); !errors.Is(err, context.Canceled) {
		t.Fatal("runner ignored cancellation")
	}
}

func TestRunnerRewindHistoryReaders(t *testing.T) {
	svc := session.InMemoryService()
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	tree := agentTree(t)
	r := &Runner{appName: "app", sessionService: svc, rootAgent: tree.root}
	ev := session.NewEvent(t.Context(), "A")
	ev.Author = tree.allowsTransferAgent.Name()
	ev.IsolationScope = "scope"
	ev.LongRunningToolIDs = []string{"call"}
	ev.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "approve"}}}}
	if err := svc.AppendEvent(t.Context(), created.Session, ev); err != nil {
		t.Fatal(err)
	}
	response := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "approve"}}}}
	if got, err := r.findAgentToRun(created.Session, response); err != nil || got != tree.allowsTransferAgent {
		t.Fatal("fixture did not select the sub-agent")
	}
	if resolveInvocationID(created.Session, response) != "A" || len(openLongRunningCallIDs(created.Session)) != 1 || len(answeredInterrupts(created.Session, response)) != 1 || findActiveTaskIsolationScope(created.Session) != "scope" {
		t.Fatal("fixture did not expose the pending call")
	}
	if err := r.Rewind(t.Context(), "u", "s", "A"); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []*genai.Content{nil, response} {
		if got, err := r.findAgentToRun(fresh.Session, msg); err != nil || got != tree.root {
			t.Error("rewound history selected a sub-agent")
		}
	}
	if resolveInvocationID(fresh.Session, response) != "" {
		t.Error("rewound invocation was resumed")
	}
	if len(openLongRunningCallIDs(fresh.Session)) != 0 {
		t.Error("rewound call is still pending")
	}
	if len(answeredInterrupts(fresh.Session, response)) != 0 {
		t.Error("rewound interrupt was answered")
	}
	if findActiveTaskIsolationScope(fresh.Session) != "" {
		t.Error("rewound isolation scope was reused")
	}
}

func TestRunnerRewindCompaction(t *testing.T) {
	m := &scriptedModel{replyFmt: "answer %d"}
	summarizer := &rewindSummarizer{}
	r, svc := newCompactionRunner(t, m, &compaction.Config{CompactionInterval: 2, Summarizer: summarizer})
	turn := func(text string) {
		t.Helper()
		drain(t, r.Run(t.Context(), "u", "s", genai.NewContentFromText(text, genai.RoleUser), agent.RunConfig{}))
	}
	turn("retained")
	turn("discarded")
	if len(summarizer.windows) != 1 || !strings.Contains(summarizer.windows[0], "discarded") {
		t.Fatal("fixture did not compact the discarded turn")
	}
	var target string
	for ev := range getSession(t, svc, "u", "s").Events().All() {
		if ev.Content != nil && ev.Content.Role == genai.RoleUser {
			target = ev.InvocationID
		}
	}
	if err := r.Rewind(t.Context(), "u", "s", target); err != nil {
		t.Fatal(err)
	}
	turn("replacement")
	prompt := promptText(m.lastPrompt())
	if strings.Contains(prompt, "discarded") || !strings.Contains(prompt, "retained") || !strings.Contains(prompt, "replacement") {
		t.Fatal("rewind left stale summary content in the model request")
	}
	if len(summarizer.windows) != 2 {
		t.Fatal("compaction did not resume on the active history")
	}
	if strings.Contains(summarizer.windows[1], "discarded") || !strings.Contains(summarizer.windows[1], "retained") || !strings.Contains(summarizer.windows[1], "replacement") {
		t.Fatal("compaction summarized discarded history")
	}
	turn("next")
	if prompt := promptText(m.lastPrompt()); strings.Contains(prompt, "discarded") || !strings.Contains(prompt, "replacement") {
		t.Fatal("a later model request reused a discarded summary")
	}
}

type rewindSummarizer struct{ windows []string }

func (s *rewindSummarizer) SummarizeEvents(_ context.Context, events []*session.Event) (compaction.SummarizeResult, error) {
	var contents []*genai.Content
	for _, ev := range events {
		if ev.Content != nil {
			contents = append(contents, ev.Content)
		}
	}
	text := promptText(contents)
	s.windows = append(s.windows, text)
	return compaction.SummarizeResult{Content: genai.NewContentFromText(text, genai.RoleModel)}, nil
}
