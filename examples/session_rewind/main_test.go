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

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRewindDemo(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"-demo"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Count(text, "Model context:") != 5 || strings.Count(text, "Rewound before ") != 3 {
		t.Fatal("demo did not execute five turns and three rewinds")
	}
	if !strings.Contains(text, "Raw events: 13 | Active events: 2") {
		t.Fatal("demo lost raw history or retained discarded events")
	}
	lastState := text[strings.LastIndex(text, "Stored state:"):]
	if strings.Contains(lastState, `"booking"`) || strings.Contains(lastState, `"answer"`) || !strings.Contains(lastState, `"city": "Shanghai"`) {
		t.Fatal("demo did not restore initial stored state")
	}
	lastPrompt := text[strings.LastIndex(text, "Model context:"):]
	if strings.Contains(lastPrompt, "Plan a trip.") || !strings.Contains(lastPrompt, "user: Start again.") {
		t.Fatal("demo did not reset model context")
	}
}

func TestRewindConsoleRecoversFromInputErrors(t *testing.T) {
	var output bytes.Buffer
	input := "rewind last\nset city invalid\nsay\nsay hello\nrewind missing\nrewind first\nstate\nquit\n"
	if err := run(t.Context(), nil, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "Error:") != 4 {
		t.Fatal("console did not report input errors")
	}
	if !strings.Contains(output.String(), `"city": "Shanghai"`) {
		t.Fatal("console stopped before restoring the session")
	}
}
