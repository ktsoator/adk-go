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

package llmagent

import (
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/internal/workflowinternal"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

func TestRewindExcludesPendingTaskDelegations(t *testing.T) {
	svc := session.InMemoryService()
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ev := session.NewEvent(t.Context(), "A")
	ev.Author = "assistant"
	ev.Content = &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "task"}}}}
	if err := svc.AppendEvent(t.Context(), created.Session, ev); err != nil {
		t.Fatal(err)
	}
	tools := map[string]tool.Tool{"task": &workflowinternal.TaskAgentTool{}}
	if len(findUnresolvedTaskDelegations(created.Session, "assistant", tools)) != 1 {
		t.Fatal("fixture did not expose pending delegation")
	}
	if err := svc.(session.RewindService).Rewind(t.Context(), &session.RewindRequest{AppName: "app", UserID: "u", SessionID: "s", BeforeInvocationID: "A"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(findUnresolvedTaskDelegations(fresh.Session, "assistant", tools)) != 0 {
		t.Fatal("rewound task delegation was resumed")
	}
}
