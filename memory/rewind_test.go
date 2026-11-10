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

package memory_test

import (
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

func TestMemoryRewindExcludesEvents(t *testing.T) {
	sess := makeSession(t, "app", "u", "s", []*session.Event{
		{InvocationID: "A", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("retained", genai.RoleUser)}},
		{InvocationID: "B", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("discarded", genai.RoleUser)}},
		{Actions: session.EventActions{RewindBeforeInvocationID: "B"}},
	})
	svc := memory.InMemoryService()
	if err := svc.AddSessionToMemory(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"retained", 1}, {"discarded", 0}} {
		res, err := svc.SearchMemory(t.Context(), &memory.SearchRequest{AppName: "app", UserID: "u", Query: tc.query})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Memories) != tc.count {
			t.Error("memory ingestion did not use active history")
		}
	}
}
