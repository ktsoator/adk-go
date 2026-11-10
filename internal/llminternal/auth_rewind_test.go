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

package llminternal

import (
	"maps"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/internal/utils"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolauth"
)

func TestRewindDoesNotAcceptDiscardedCredentialRequest(t *testing.T) {
	for _, rewound := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "rewound"}[rewound], func(t *testing.T) {
			svc := session.InMemoryService()
			created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u"})
			if err != nil {
				t.Fatal(err)
			}
			sess := created.Session
			request := session.NewEvent(t.Context(), "A")
			request.Author = "worker"
			request.Content = genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID: "credential", Name: toolauth.RequestCredentialFunctionCallName,
				Args: map[string]any{
					"functionCallId": "original",
					"authConfig": map[string]any{
						"authScheme":    map[string]any{"type": "apiKey", "in": "header", "name": "X-Key"},
						"credentialKey": "test",
					},
				},
			}}}, genai.RoleModel)
			if err := svc.AppendEvent(t.Context(), sess, request); err != nil {
				t.Fatal(err)
			}
			if rewound {
				if err := svc.(session.RewindService).Rewind(t.Context(), &session.RewindRequest{AppName: "app", UserID: "u", SessionID: sess.ID(), BeforeInvocationID: "A"}); err != nil {
					t.Fatal(err)
				}
				fresh, err := svc.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: sess.ID()})
				if err != nil {
					t.Fatal(err)
				}
				sess = fresh.Session
			}
			answer := session.NewEvent(t.Context(), "B")
			answer.Author = "user"
			answer.Content = genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID: "credential", Name: toolauth.RequestCredentialFunctionCallName,
				Response: map[string]any{"authType": "apiKey", "apiKey": "synthetic-test-value"},
			}}}, genai.RoleUser)
			if err := svc.AppendEvent(t.Context(), sess, answer); err != nil {
				t.Fatal(err)
			}
			ctx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{
				Agent: &mockLLMAgent{Agent: utils.Must(agent.New(agent.Config{Name: "worker"})), s: &State{}}, Session: sess,
			})
			for _, err := range authPreprocessor(ctx, &model.LLMRequest{}, &Flow{}) {
				if err != nil {
					t.Fatal("credential preprocessing failed")
				}
			}
			if stored := len(maps.Collect(sess.State().All())) != 0; stored == rewound {
				t.Fatal("credential acceptance did not follow active history")
			}
		})
	}
}
