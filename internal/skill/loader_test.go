package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// —————— Load 单文件测试 ——————

func TestLoadValidSkill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-skill.md")
	content := `---
name: test-skill
description: 测试 skill
approval_level: L0
side_effect: read
idempotent: true
parameters:
  - {name: input, type: string, required: true}
---
# test-skill
这是测试 skill 的正文。
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader()
	s, err := loader.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if s.Name != "test-skill" {
		t.Errorf("expected 'test-skill', got %s", s.Name)
	}
	if s.Description != "测试 skill" {
		t.Errorf("description mismatch: %s", s.Description)
	}
	if s.ApprovalLevel != "L0" {
		t.Errorf("expected L0, got %s", s.ApprovalLevel)
	}
	if s.SideEffect != "read" {
		t.Errorf("expected read, got %s", s.SideEffect)
	}
	if !s.Idempotent {
		t.Error("expected idempotent=true")
	}
	if len(s.Parameters) != 1 {
		t.Errorf("expected 1 param, got %d", len(s.Parameters))
	}
	if s.Parameters[0].Name != "input" {
		t.Errorf("expected param name 'input', got %s", s.Parameters[0].Name)
	}
	if !s.Parameters[0].Required {
		t.Error("expected param required=true")
	}
	if s.Body == "" {
		t.Error("body is empty")
	}
}

func TestLoadAll(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"skill-a.md": `---
name: skill-a
description: desc a
approval_level: L0
side_effect: read
idempotent: true
parameters: []
---
body a
`,
		"skill-b.md": `---
name: skill-b
description: desc b
approval_level: L2
side_effect: write
idempotent: false
parameters:
  - {name: x, type: integer, required: true}
---
body b
`,
		"not-a-skill.txt": "not a skill file",
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	loader := NewLoader()
	skills, err := loader.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(skills))
	}
}

func TestLoadAllFromActualSkillsDir(t *testing.T) {
	// 测试从项目实际的 .skills/ 目录加载
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Skipf("cannot find project root: %v", err)
	}

	skillsDir := filepath.Join(projectRoot, ".skills")
	if _, err := os.Stat(skillsDir); os.IsNotExist(err) {
		t.Skipf(".skills/ directory not found at %s", skillsDir)
	}

	loader := NewLoader()
	skills, err := loader.LoadAll(skillsDir)
	if err != nil {
		t.Fatalf("LoadAll .skills/: %v", err)
	}

	// 预期 17 个 skill（含 P2-1 新增的 deploy-profiling, cleanup-profiling）
	if len(skills) != 17 {
		t.Errorf("expected 15 skills, got %d", len(skills))
	}

	// 验证所有 skill 都有必填字段
	for _, s := range skills {
		if s.Name == "" {
			t.Error("skill has empty name")
		}
		if s.ApprovalLevel == "" {
			t.Errorf("skill %s has empty approval_level", s.Name)
		}
		if s.SideEffect == "" {
			t.Errorf("skill %s has empty side_effect", s.Name)
		}
		if !isValidApprovalLevel(s.ApprovalLevel) {
			t.Errorf("skill %s has invalid approval_level: %s", s.Name, s.ApprovalLevel)
		}
	}
}

func TestLoadMissingName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	content := `---
description: missing name
approval_level: L0
side_effect: read
idempotent: true
parameters: []
---
body
`
	os.WriteFile(path, []byte(content), 0o644)

	loader := NewLoader()
	_, err := loader.Load(path)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestLoadMissingApprovalLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	content := `---
name: bad
description: missing approval
side_effect: read
idempotent: true
parameters: []
---
body
`
	os.WriteFile(path, []byte(content), 0o644)

	loader := NewLoader()
	_, err := loader.Load(path)
	if err == nil {
		t.Error("expected error for missing approval_level")
	}
}

func TestLoadMissingSideEffect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	content := `---
name: bad
description: missing side_effect
approval_level: L0
idempotent: true
parameters: []
---
body
`
	os.WriteFile(path, []byte(content), 0o644)

	loader := NewLoader()
	_, err := loader.Load(path)
	if err == nil {
		t.Error("expected error for missing side_effect")
	}
}

func TestLoadInvalidApprovalLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	content := `---
name: bad
description: bad level
approval_level: L5
side_effect: read
idempotent: true
parameters: []
---
body
`
	os.WriteFile(path, []byte(content), 0o644)

	loader := NewLoader()
	_, err := loader.Load(path)
	if err == nil {
		t.Error("expected error for invalid approval_level L5")
	}
}

func TestLoadNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nofm.md")
	os.WriteFile(path, []byte("just body, no frontmatter"), 0o644)

	loader := NewLoader()
	_, err := loader.Load(path)
	if err == nil {
		t.Error("expected error for missing frontmatter")
	}
}

// —————— 辅助 ——————

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
