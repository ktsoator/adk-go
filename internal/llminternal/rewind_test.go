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

package llminternal_test

import (
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/internal/llminternal"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

func TestRewindDoesNotResumeDiscardedConfirmation(t *testing.T) {
	call := &session.Event{InvocationID: "A", Author: "testAgent"}
	call.Content = genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{
		Name: toolconfirmation.FunctionCallName, ID: "confirmation",
		Args: map[string]any{
			"originalFunctionCall": map[string]any{"name": mockToolName, "id": "original", "args": map[string]any{}},
			"toolConfirmation":     toolconfirmation.ToolConfirmation{Confirmed: false},
		},
	}}}, genai.RoleModel)
	response := &session.Event{InvocationID: "B", Author: "user"}
	response.Content = genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		Name: toolconfirmation.FunctionCallName, ID: "confirmation", Response: map[string]any{"confirmed": true},
	}}}, genai.RoleUser)
	ag, tools, err := newMockLlmAgent()
	if err != nil {
		t.Fatal(err)
	}
	for _, rewound := range []bool{false, true} {
		raw := []*session.Event{call}
		if rewound {
			raw = append(raw, &session.Event{Author: "user", Actions: session.EventActions{RewindBeforeInvocationID: "A"}})
		}
		raw = append(raw, response)
		ctx := createInvocationContext(t, ag, &fakeSession{events: raw})
		count := 0
		for _, err := range llminternal.RequestConfirmationRequestProcessor(ctx, &model.LLMRequest{}, &llminternal.Flow{Tools: tools}) {
			if err != nil {
				t.Fatal(err)
			}
			count++
		}
		want := 1
		if rewound {
			want = 0
		}
		if count != want {
			t.Fatal("confirmation dispatch did not follow active history")
		}
	}
}
