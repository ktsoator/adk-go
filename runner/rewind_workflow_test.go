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

package runner_test

import (
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

func TestRewindDoesNotRouteToDiscardedWorkflowCall(t *testing.T) {
	for _, graph := range []bool{false, true} {
		for _, rewound := range []bool{false, true} {
			name := map[bool]string{false: "agent", true: "graph"}[graph] + "/" + map[bool]string{false: "active", true: "rewound"}[rewound]
			t.Run(name, func(t *testing.T) {
				var otherRuns, callerRuns atomic.Int32
				other := customAgent(t, "other", &otherRuns, nil)
				caller := customAgent(t, "caller", &callerRuns, nil)
				child := caller
				if graph {
					var err error
					child, err = workflowagent.New(workflowagent.Config{Name: "wf", SubAgents: []agent.Agent{caller}, Edges: []workflow.Edge{{From: workflow.Start, To: joinResumeAgentNode(t, caller)}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				svc := session.InMemoryService()
				created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u"})
				if err != nil {
					t.Fatal(err)
				}
				r, err := runner.New(runner.Config{AppName: "app", Agent: mustSequential(t, "root", other, child), SessionService: svc})
				if err != nil {
					t.Fatal(err)
				}
				call := session.NewEvent(t.Context(), "A")
				call.Author = "caller"
				call.Content = genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "old-call", Name: "approve"}}}, genai.RoleModel)
				if err := svc.AppendEvent(t.Context(), created.Session, call); err != nil {
					t.Fatal(err)
				}
				if rewound {
					if err := r.Rewind(t.Context(), "u", created.Session.ID(), "A"); err != nil {
						t.Fatal(err)
					}
				}
				response := genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "old-call", Name: "approve", Response: map[string]any{"ok": true}}}}, genai.RoleUser)
				for _, err := range r.Run(t.Context(), "u", created.Session.ID(), response, agent.RunConfig{}) {
					if err != nil {
						t.Fatal(err)
					}
				}
				wantOther := int32(0)
				if rewound {
					wantOther = 1
				}
				if otherRuns.Load() != wantOther || callerRuns.Load() != 1 {
					t.Fatal("workflow routing did not follow active history")
				}
			})
		}
	}
}
