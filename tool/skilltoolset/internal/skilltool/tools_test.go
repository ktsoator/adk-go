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

package skilltool_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/internal/toolinternal"
	"google.golang.org/adk/v2/tool/skilltoolset/internal/skilltool"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

type mockSource struct {
	frontmatters []*skill.Frontmatter
	resources    map[string]map[string]string
	instructions map[string]string
}

func (m *mockSource) ListFrontmatters(ctx context.Context) ([]*skill.Frontmatter, error) {
	return m.frontmatters, nil
}

func (m *mockSource) ListResources(ctx context.Context, name, subpath string) ([]string, error) {
	var res []string
	for p := range m.resources[name] {
		if strings.HasPrefix(p, subpath) {
			res = append(res, p)
		}
	}
	return res, nil
}

func (m *mockSource) LoadFrontmatter(ctx context.Context, name string) (*skill.Frontmatter, error) {
	for _, fm := range m.frontmatters {
		if fm.Name == name {
			return fm, nil
		}
	}
	return nil, skill.ErrSkillNotFound
}

func (m *mockSource) LoadInstructions(ctx context.Context, name string) (string, error) {
	inst, ok := m.instructions[name]
	if !ok {
		return "", skill.ErrSkillNotFound
	}
	return inst, nil
}

func (m *mockSource) LoadResource(ctx context.Context, name, resourcePath string) (io.ReadCloser, error) {
	res, ok := m.resources[name][resourcePath]
	if !ok {
		return nil, skill.ErrResourceNotFound
	}
	return io.NopCloser(strings.NewReader(res)), nil
}

func createToolContext(t *testing.T) agent.Context {
	invCtx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{})
	return agent.NewToolContext(invCtx, "", nil, nil)
}

func TestListSkills(t *testing.T) {
	source := &mockSource{
		frontmatters: []*skill.Frontmatter{
			{Name: "skill1", Description: "description1"},
			{Name: "skill2", Description: "description2"},
		},
	}

	tTool, err := skilltool.ListSkills(source)
	if err != nil {
		t.Fatalf("ListSkills failed: %v", err)
	}

	funcTool, ok := tTool.(toolinternal.FunctionTool)
	if !ok {
		t.Fatal("tool does not implement toolinternal.FunctionTool")
	}

	result, err := funcTool.Run(createToolContext(t), map[string]any{})
	if err != nil {
		t.Fatalf("tool.Run failed: %v", err)
	}

	want := map[string]any{
		"result": "<available_skills>\n<skill>\n<name>\nskill1\n</name>\n<description>\ndescription1\n</description>\n</skill>\n<skill>\n<name>\nskill2\n</name>\n<description>\ndescription2\n</description>\n</skill>\n</available_skills>",
	}

	if diff := cmp.Diff(want, result); diff != "" {
		t.Errorf("result mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadSkill(t *testing.T) {
	source := &mockSource{
		frontmatters: []*skill.Frontmatter{
			{
				Name:        "skill1",
				Description: "description1",
				Metadata: map[string]string{
					"key1": "value1",
				},
			},
		},
		instructions: map[string]string{
			"skill1": "instructions1",
		},
	}
	tool, err := skilltool.LoadSkill(source)
	if err != nil {
		t.Fatalf("LoadSkill failed: %v", err)
	}
	functionTool, ok := tool.(toolinternal.FunctionTool)
	if !ok {
		t.Fatal("LoadSkill tool does not implement toolinternal.FunctionTool")
	}

	got, err := functionTool.Run(createToolContext(t), map[string]any{"name": "skill1"})
	if err != nil {
		t.Fatalf("LoadSkill tool.Run failed: %v", err)
	}
	want := map[string]any{
		"skill_name":   "skill1",
		"instructions": "instructions1",
		"frontmatter": map[string]any{
			"name":        "skill1",
			"description": "description1",
			"metadata": map[string]any{
				"key1": "value1",
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LoadSkill result mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadSkillResources(t *testing.T) {
	files := fstest.MapFS{
		"skill1/SKILL.md":                   {Data: []byte("---\nname: skill1\ndescription: A test skill\n---\nInstructions.")},
		"skill1/references/nested/guide.md": {Data: []byte("Reference.")},
		"skill1/assets/template.json":       {Data: []byte("{}")},
		"skill1/scripts/check.py":           {Data: []byte("pass")},
		"skill1/unlisted.txt":               {Data: []byte("Not a skill resource.")},
		"empty/SKILL.md":                    {Data: []byte("---\nname: empty\ndescription: A skill without resources\n---\nInstructions.")},
	}
	base := skill.NewFileSystemSource(files)
	frontmatterPreload, _, err := skill.WithFrontmatterPreloadSource(t.Context(), base)
	if err != nil {
		t.Fatalf("WithFrontmatterPreloadSource failed: %v", err)
	}
	completePreload, _, err := skill.WithCompletePreloadSource(t.Context(), base)
	if err != nil {
		t.Fatalf("WithCompletePreloadSource failed: %v", err)
	}
	sources := []struct {
		name   string
		source skill.Source
	}{
		{name: "filesystem", source: base},
		{name: "frontmatter_preload", source: frontmatterPreload},
		{name: "complete_preload", source: completePreload},
		{name: "merged", source: skill.NewMergedSource(skill.NewFileSystemSource(fstest.MapFS{}), base)},
	}
	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			load, err := skilltool.LoadSkill(source.source)
			if err != nil {
				t.Fatalf("LoadSkill failed: %v", err)
			}
			loadResource, err := skilltool.LoadSkillResource(source.source)
			if err != nil {
				t.Fatalf("LoadSkillResource failed: %v", err)
			}
			for _, name := range []string{"skill1", "empty"} {
				t.Run(name, func(t *testing.T) {
					ctx := createToolContext(t)
					got, err := load.(toolinternal.FunctionTool).Run(ctx, map[string]any{"name": name})
					if err != nil {
						t.Fatalf("load_skill failed: %v", err)
					}
					resources, exists := got["resources"]
					if name == "empty" {
						if exists {
							t.Fatal("resources must be omitted for a skill without resources")
						}
						return
					}
					paths, ok := resources.([]any)
					if !ok {
						t.Fatalf("resources has type %T, want []any", resources)
					}
					want := map[string]string{
						"references/nested/guide.md": "Reference.",
						"assets/template.json":       "{}",
						"scripts/check.py":           "pass",
					}
					if len(paths) != len(want) {
						t.Fatalf("got %d resource paths, want %d", len(paths), len(want))
					}
					for _, resource := range paths {
						resourcePath, ok := resource.(string)
						if !ok {
							t.Fatalf("resource path has type %T, want string", resource)
						}
						content, exists := want[resourcePath]
						if !exists {
							t.Fatal("load_skill returned an unexpected or duplicate resource path")
						}
						result, err := loadResource.(toolinternal.FunctionTool).Run(ctx, map[string]any{
							"skill_name": name, "resource_path": resourcePath,
						})
						if err != nil {
							t.Fatalf("load_skill_resource failed: %v", err)
						}
						if result["content"] != content {
							t.Error("loaded resource content does not match the listed resource")
						}
						delete(want, resourcePath)
					}
				})
			}
		})
	}
}

type resourceListingErrorSource struct {
	skill.Source
	err error
}

func (s *resourceListingErrorSource) ListResources(context.Context, string, string) ([]string, error) {
	return nil, s.err
}

func TestLoadSkillResourceListingError(t *testing.T) {
	wantErr := errors.New("resource listing failed")
	source := &resourceListingErrorSource{
		Source: &mockSource{
			frontmatters: []*skill.Frontmatter{{Name: "skill1", Description: "A test skill"}},
			instructions: map[string]string{"skill1": "Instructions."},
		},
		err: wantErr,
	}
	load, err := skilltool.LoadSkill(source)
	if err != nil {
		t.Fatalf("LoadSkill failed: %v", err)
	}
	got, err := load.(toolinternal.FunctionTool).Run(createToolContext(t), map[string]any{"name": "skill1"})
	if !errors.Is(err, wantErr) {
		t.Errorf("load_skill error = %v, want wrapped listing error", err)
	}
	if err != nil && !strings.Contains(err.Error(), "list resources") {
		t.Error("load_skill error does not identify resource listing")
	}
	if got != nil {
		t.Error("load_skill returned a partial result after resource listing failed")
	}
}

func TestLoadSkillResource(t *testing.T) {
	source := &mockSource{
		resources: map[string]map[string]string{
			"skill1": {"assets/data.txt": "content1"},
		},
	}
	tool, err := skilltool.LoadSkillResource(source)
	if err != nil {
		t.Fatalf("LoadSkillResource: %v", err)
	}
	functionTool, ok := tool.(toolinternal.FunctionTool)
	if !ok {
		t.Fatalf("LoadSkillResource: tool does not implement toolinternal.FunctionTool")
	}
	result, err := functionTool.Run(createToolContext(t), map[string]any{
		"skill_name":    "skill1",
		"resource_path": "assets/data.txt",
	})
	if err != nil {
		t.Fatalf("tool.Run failed: %v", err)
	}
	want := map[string]any{
		"skill_name": "skill1",
		"path":       "assets/data.txt",
		"content":    "content1",
	}
	if diff := cmp.Diff(want, result); diff != "" {
		t.Errorf("result mismatch (-want +got):\n%s", diff)
	}
}
