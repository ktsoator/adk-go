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

// Package main demonstrates native session rewind with an offline model.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"log"
	"maps"
	"os"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

func main() {
	log.SetFlags(0)
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("session_rewind", flag.ContinueOnError)
	flags.SetOutput(output)
	demo := flags.Bool("demo", false, "run a reproducible five-turn scenario")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *demo {
		var next int
		ctx = platform.WithUUIDProvider(ctx, func() string { next++; return fmt.Sprintf("id-%03d", next) })
	}
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{
		AppName: "rewind-demo", UserID: "user", SessionID: "conversation",
		State: map[string]any{"city": "Shanghai", "language": "zh", "nullable": nil},
	})
	if err != nil {
		return err
	}
	a, err := llmagent.New(llmagent.Config{Name: "assistant", Model: &echoModel{output: output}, OutputKey: "answer"})
	if err != nil {
		return err
	}
	r, err := runner.New(runner.Config{AppName: "rewind-demo", Agent: a, SessionService: svc})
	if err != nil {
		return err
	}
	c := &console{runner: r, service: svc, output: output, pending: map[string]any{}, request: session.GetRequest{
		AppName: "rewind-demo", UserID: "user", SessionID: created.Session.ID(),
	}}
	_, _ = fmt.Fprintln(output, "Native session rewind prototype | in-memory service | offline echo model")
	_, _ = fmt.Fprintln(output, "Commands: say TEXT, set KEY JSON, state, history, rewind ID|first|last, help, quit")
	if *demo {
		for _, line := range []string{
			"state", `set city "Hangzhou"`, "say Plan a trip.",
			`set city "Beijing"`, "set booking true", "say Pick a morning flight.",
			"state", "rewind last", "state",
			`set city "Shenzhen"`, "say Pick an afternoon flight.",
			"rewind last", "say Compare trains.", "rewind first", "state", "say Start again.", "history",
		} {
			_, _ = fmt.Fprintf(output, "\n> %s\n", line)
			if err := c.command(ctx, line); err != nil {
				return err
			}
		}
		return nil
	}
	scanner := bufio.NewScanner(input)
	for {
		_, _ = fmt.Fprint(output, "\n> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "quit" {
			return nil
		}
		if err := c.command(ctx, line); err != nil {
			_, _ = fmt.Fprintf(output, "Error: %v\n", err)
		}
	}
}

type console struct {
	runner  *runner.Runner
	service session.Service
	request session.GetRequest
	pending map[string]any
	output  io.Writer
}

func (c *console) command(ctx context.Context, line string) error {
	command, argument, _ := strings.Cut(line, " ")
	argument = strings.TrimSpace(argument)
	switch command {
	case "", "help":
		_, _ = fmt.Fprintln(c.output, "set stages a state change for the next say. rewind keeps raw history and restores stored session state.")
		_, _ = fmt.Fprintln(c.output, "Use history to find invocation IDs. first/last select from the active user turns. quit exits.")
		_, _ = fmt.Fprintln(c.output, "This process uses memory only. Artifacts and external tool effects are not restored.")
	case "set":
		key, encoded, ok := strings.Cut(argument, " ")
		if !ok || key == "" {
			return fmt.Errorf("usage: set KEY JSON")
		}
		var value any
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			return fmt.Errorf("state value must be valid JSON")
		}
		c.pending[key] = value
		_, _ = fmt.Fprintln(c.output, "State change staged for the next turn.")
	case "say":
		if argument == "" {
			return fmt.Errorf("usage: say TEXT")
		}
		var invocationID string
		for ev, err := range c.runner.Run(ctx, c.request.UserID, c.request.SessionID,
			genai.NewContentFromText(argument, genai.RoleUser), agent.RunConfig{}, runner.WithStateDelta(c.pending)) {
			if err != nil {
				return err
			}
			invocationID = ev.InvocationID
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						_, _ = fmt.Fprintf(c.output, "Assistant: %s\n", part.Text)
					}
				}
			}
		}
		c.pending = map[string]any{}
		_, _ = fmt.Fprintf(c.output, "Invocation: %s\n", invocationID)
	case "state":
		res, err := c.service.Get(ctx, &c.request)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(maps.Collect(res.Session.State().All()), "", "  ")
		if err != nil {
			return fmt.Errorf("encode stored state: %w", err)
		}
		_, _ = fmt.Fprintf(c.output, "Stored state:\n%s\n", encoded)
	case "history":
		res, err := c.service.Get(ctx, &c.request)
		if err != nil {
			return err
		}
		active := session.ActiveEvents(res.Session.Events())
		ids := map[string]bool{}
		for ev := range active.All() {
			ids[ev.ID] = true
		}
		_, _ = fmt.Fprintf(c.output, "Raw events: %d | Active events: %d\n", res.Session.Events().Len(), active.Len())
		for ev := range res.Session.Events().All() {
			status := "rewound"
			if ids[ev.ID] {
				status = "active"
			}
			if target := ev.Actions.RewindBeforeInvocationID; target != "" {
				_, _ = fmt.Fprintf(c.output, "  %s  marker   before=%s\n", ev.ID, target)
			} else {
				_, _ = fmt.Fprintf(c.output, "  %s  %-8s invocation=%s author=%s\n", ev.ID, status, ev.InvocationID, ev.Author)
			}
		}
	case "rewind":
		if argument == "" {
			return fmt.Errorf("usage: rewind ID|first|last")
		}
		target := argument
		if argument == "first" || argument == "last" {
			res, err := c.service.Get(ctx, &c.request)
			if err != nil {
				return err
			}
			target = ""
			for ev := range session.ActiveEvents(res.Session.Events()).All() {
				if ev.Author == "user" {
					target = ev.InvocationID
					if argument == "first" {
						break
					}
				}
			}
			if target == "" {
				return fmt.Errorf("no active user turn to rewind")
			}
		}
		if err := c.runner.Rewind(ctx, c.request.UserID, c.request.SessionID, target); err != nil {
			return err
		}
		c.pending = map[string]any{}
		_, _ = fmt.Fprintf(c.output, "Rewound before %s. Stored state restored; pending changes cleared.\n", target)
	default:
		return fmt.Errorf("unknown command; use help")
	}
	return nil
}

// echoModel prints the actual request and responds without network access.
type echoModel struct{ output io.Writer }

func (*echoModel) Name() string { return "offline-echo" }

func (m *echoModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		_, _ = fmt.Fprintln(m.output, "Model context:")
		var lastUser string
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				_, _ = fmt.Fprintf(m.output, "  %s: %s\n", content.Role, part.Text)
				if content.Role == genai.RoleUser {
					lastUser = part.Text
				}
			}
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Acknowledged: "+lastUser, genai.RoleModel)}, nil)
	}
}
