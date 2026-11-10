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

package vertexai

import (
	"errors"
	"testing"

	"google.golang.org/adk/v2/session"
)

func TestAppendEventRejectsRewindMarker(t *testing.T) {
	created, err := session.InMemoryService().Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	ev := session.NewEvent(t.Context(), "inv")
	ev.Actions.RewindBeforeInvocationID = "target"
	svc := &vertexAiService{}
	if err := svc.AppendEvent(t.Context(), created.Session, ev); !errors.Is(err, session.ErrRewindUnsupported) {
		t.Fatal("backend accepted a marker without restoring state")
	}
}
