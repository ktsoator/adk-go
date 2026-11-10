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
	"context"
	"fmt"

	"google.golang.org/adk/v2/session"
)

// Rewind restores a session to immediately before beforeInvocationID. The raw
// events remain available through the session service; subsequent runs use the
// history returned by [session.ActiveEvents]. App and user state are unchanged.
//
// The session service must implement [session.RewindService]. InMemoryService
// supports this prototype; database and Vertex AI services return
// [session.ErrRewindUnsupported]. Artifact restoration is not implemented:
// discarding an event with an artifact delta fails without changing the session.
// External tool effects and memories already written elsewhere are not undone.
// A later remote-agent call cannot reuse an A2A task or context from before a
// rewind; start a fresh remote context instead.
//
// Finish or cancel and drain the previous run before calling Rewind. It does
// not cancel running tools or model calls. Handles fetched before the rewind
// cannot append afterward; fetch a fresh session to start another invocation.
func (r *Runner) Rewind(ctx context.Context, userID, sessionID, beforeInvocationID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	svc, ok := r.sessionService.(session.RewindService)
	if !ok {
		return session.ErrRewindUnsupported
	}
	if err := svc.Rewind(ctx, &session.RewindRequest{
		AppName: r.appName, UserID: userID, SessionID: sessionID, BeforeInvocationID: beforeInvocationID,
	}); err != nil {
		return fmt.Errorf("rewind session: %w", err)
	}
	return nil
}
