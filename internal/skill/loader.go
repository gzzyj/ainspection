package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loader Skill 文件加载接口。
type Loader interface {
	Load(path string) (*Skill, error)
	LoadAll(dir string) ([]*Skill, error)
}

type loaderImpl struct{}

// NewLoader 创建 Loader 实例。
func NewLoader() Loader {
	return &loaderImpl{}
}

// skillFrontmatter 对应 .skills/*.md 的 YAML frontmatter。
type skillFrontmatter struct {
	Name          string      `yaml:"name"`
	Description   string      `yaml:"description"`
	ApprovalLevel string      `yaml:"approval_level"`
	SideEffect    string      `yaml:"side_effect"`
	Idempotent    bool        `yaml:"idempotent"`
	Parameters    []Parameter `yaml:"parameters"`
	InjectionMode string      `yaml:"injection_mode"`
}

// Load 从单个 .md 文件加载 Skill。
func (l *loaderImpl) Load(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill file %s: %w", path, err)
	}

	content := string(data)

	// 解析 YAML frontmatter（--- ... ---）
	fm, body, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// 校验必填字段
	if fm.Name == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 name", path)
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 description", path)
	}
	if fm.ApprovalLevel == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 approval_level", path)
	}
	if fm.SideEffect == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 side_effect", path)
	}
	// idempotent 默认 false，不强制校验

	return &Skill{
		Name:          fm.Name,
		Description:   fm.Description,
		Parameters:    fm.Parameters,
		Body:          strings.TrimSpace(body),
		SourcePath:    path,
		ApprovalLevel: fm.ApprovalLevel,
		SideEffect:    fm.SideEffect,
		Idempotent:    fm.Idempotent,
		InjectionMode: fm.InjectionMode,
	}, nil
}

// LoadAll 从目录加载所有 .md 文件。
func (l *loaderImpl) LoadAll(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skill dir %s: %w", dir, err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		s, err := l.Load(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}

	return skills, nil
}

// parseFrontmatter 解析 YAML frontmatter + 返回 body。
func parseFrontmatter(content string) (*skillFrontmatter, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, "", fmt.Errorf("no frontmatter found (must start with ---)")
	}

	// 找到 closing ---
	endIdx := strings.Index(content[4:], "\n---")
	if endIdx < 0 {
		return nil, "", fmt.Errorf("unclosed frontmatter (missing closing ---)")
	}

	fmYaml := content[4 : 4+endIdx]
	body := content[4+endIdx+4:] // 跳过 \n---

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(fmYaml), &fm); err != nil {
		return nil, "", fmt.Errorf("parse frontmatter yaml: %w", err)
	}

	// 校验 approval_level 合法性
	if fm.ApprovalLevel != "" {
		if !isValidApprovalLevel(fm.ApprovalLevel) {
			return nil, "", fmt.Errorf("invalid approval_level: %s (must be L0/L1/L2/L3)", fm.ApprovalLevel)
		}
	}

	return &fm, body, nil
}

func isValidApprovalLevel(level string) bool {
	switch level {
	case "L0", "L1", "L2", "L3":
		return true
	}
	return false
}
