// Package config 提供配置加载、校验和类型定义。
// 配置结构体对应 config/config.yaml.example，使用 viper 加载，
// 支持环境变量展开（如 ${ANTHROPIC_API_KEY}）和必填字段校验。
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config 是 ainspection 的完整配置类型，对应 config.yaml.example。
type Config struct {
	// DataDir 数据根目录，所有 session/task/audit 数据均存储在此目录下。
	// 默认 ~/.ainspection。
	DataDir string `mapstructure:"data_dir"`

	// DefaultAgent 默认 agent 名称，当阶段未指定 agent 时的回退选择。
	// 默认 "claude"。
	DefaultAgent string `mapstructure:"default_agent"`

	K3S           K3SConfig              `mapstructure:"k3s"`
	Services      []ServiceConfig        `mapstructure:"services"`
	Agents        map[string]AgentConfig `mapstructure:"agents"`
	Observability ObservabilityConfig    `mapstructure:"observability"`
	GitLab        GitLabConfig           `mapstructure:"gitlab"`
	Lark          LarkConfig             `mapstructure:"lark"`
	Skills        SkillsConfig           `mapstructure:"skills"`
	Prompts       PromptsConfig          `mapstructure:"prompts"`
	Context       ContextConfig          `mapstructure:"context"`
	Security      SecurityConfig         `mapstructure:"security"`
	Evaluator     EvaluatorConfig        `mapstructure:"evaluator"`
	Planner       PlannerConfig          `mapstructure:"planner"`
	MCTS          MCTSConfig             `mapstructure:"mcts"`
	Retry         RetryConfig            `mapstructure:"retry"`
	Evals         EvalsConfig            `mapstructure:"evals"`
	Pipeline      PipelineConfig         `mapstructure:"pipeline"`
}

// DefaultDataDir 默认数据根目录。
const DefaultDataDir = "~/.ainspection"

// DefaultAgentName 默认 agent 名称。
const DefaultAgentName = "claude"

// GetDataDir 获取数据根目录，未配置时返回默认值。
func (c *Config) GetDataDir() string {
	if c.DataDir == "" {
		return DefaultDataDir
	}
	return c.DataDir
}

// GetDefaultAgent 获取默认 agent 名称，未配置时返回默认值。
func (c *Config) GetDefaultAgent() string {
	if c.DefaultAgent == "" {
		return DefaultAgentName
	}
	return c.DefaultAgent
}

// K3SConfig k3s 集群配置。
type K3SConfig struct {
	Kubeconfig string `mapstructure:"kubeconfig"`
	Context    string `mapstructure:"context"`
}

// ServiceConfig 业务服务配置。
type ServiceConfig struct {
	Name         string `mapstructure:"name"`
	RepoPath     string `mapstructure:"repo_path"`
	K8SNamespace string `mapstructure:"k8s_namespace"`
}

// AgentConfig 单个 Agent 实例的配置。
// type 字段决定通信方式: claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native
// 各 Agent Adapter 按需使用 CLI 相关字段或 HTTP 相关字段。
type AgentConfig struct {
	// ─── 通用字段 ───
	Type  string `mapstructure:"type"`  // 通信方式 (必填)
	Model string `mapstructure:"model"` // LLM 模型名 (必填)

	// ─── HTTP 通信字段 (LLM Native 及部分 CLI adapter 使用) ───
	Endpoint    string            `mapstructure:"endpoint"`     // OpenAI-compatible API 端点
	APIKey      string            `mapstructure:"api_key"`      // API Key
	APIKeyEnv   string            `mapstructure:"api_key_env"`  // 从环境变量读取 API Key (如 MOONSHOT_API_KEY)
	Timeout     int               `mapstructure:"timeout"`      // 超时秒数，默认 120
	MaxTokens   int               `mapstructure:"max_tokens"`   // token 上限，默认 4096
	Temperature float64           `mapstructure:"temperature"`  // 温度参数，默认 0.7
	Headers     map[string]string `mapstructure:"headers"`      // 自定义 HTTP headers (LLM Native 专用)

	// ─── CLI adapter 字段 ───
	CLIBinary    string   `mapstructure:"cli_binary"`    // CLI 可执行文件名 (如 claude, kimi, codex)
	SkillsDir    string   `mapstructure:"skills_dir"`    // 沙箱内 skill 目录，相对路径 (如 .claude/skills)
	HooksDir     string   `mapstructure:"hooks_dir"`     // 沙箱内 hook 目录，相对路径 (如 .claude/hooks)
	HooksFile    string   `mapstructure:"hooks_file"`    // hook 配置文件名 (如 hooks.json，Codex 专用)
	SettingsFile string   `mapstructure:"settings_file"` // LLM 配置文件名 (如 settings.json, config.toml)
	Features     map[string]bool `mapstructure:"features"`    // 功能开关 (Codex 需显式启用 skills/hooks)
	Flags        []string `mapstructure:"flags"`          // 启动参数 (如 --experimental-skills，Qwen 专用)

	// ─── LLM Native 专用 ───
	SkillInjectionMode string `mapstructure:"skill_injection_mode"` // tools | messages | both

	// ─── 工具配置 ───
	NativeTools []string `mapstructure:"native_tools"` // L2 平台原生工具名称列表
}

// ResolveType 解析 agent type。
func (a AgentConfig) ResolveType() string {
	return a.Type
}

// validTypes 合法的 agent type 值。
var validTypes = map[string]bool{
	"claude_cli":  true,
	"kimi_cli":    true,
	"codex_cli":   true,
	"qwen_cli":    true,
	"gemini_cli":  true,
	"llm_native":  true,
}

// ObservabilityConfig 可观测性数据源配置。
type ObservabilityConfig struct {
	LokiURL       string `mapstructure:"loki_url"`
	PrometheusURL string `mapstructure:"prometheus_url"`
	TempoURL      string `mapstructure:"tempo_url"`
}

// GitLabConfig GitLab 实例配置。
type GitLabConfig struct {
	Instance string `mapstructure:"instance"`
	Token    string `mapstructure:"token"`
}

// LarkConfig 飞书/Lark 配置。
type LarkConfig struct {
	AppID     string `mapstructure:"app_id"`
	AppSecret string `mapstructure:"app_secret"`
}

// SkillsConfig Skill 加载配置。
type SkillsConfig struct {
	Path    string `mapstructure:"path"`
	Adapter string `mapstructure:"adapter"`
}

// PromptsConfig Prompt 模板配置。
type PromptsConfig struct {
	Path string `mapstructure:"path"`
}

// ContextConfig 树/上下文配置。
type ContextConfig struct {
	ThresholdPct    int `mapstructure:"threshold_pct"`
	MaxDepth        int `mapstructure:"max_depth"`
	SummaryMaxChars int `mapstructure:"summary_max_chars"`
}

// SecurityConfig Agent 安全配置。
type SecurityConfig struct {
	AllowedCommands  []CommandRule `mapstructure:"allowed_commands"`
	BlockedPatterns  []string      `mapstructure:"blocked_patterns"`
	BaselineCommands []string      `mapstructure:"baseline_commands"` // 基线验证命令列表，默认 go build + go vet
	Filesystem       FSConfig      `mapstructure:"filesystem"`
	Sandbox          SandboxConfig `mapstructure:"sandbox"`
	Audit            AuditConfig   `mapstructure:"audit"`
	CommandTimeout   int           `mapstructure:"command_timeout"` // 命令执行超时秒数，默认 60
}

// DefaultBaselineCommands 默认基线验证命令列表。
var DefaultBaselineCommands = []string{"go build ./...", "go vet ./..."}

// GetBaselineCommands 获取基线命令列表，未配置时返回默认值。
func (s SecurityConfig) GetBaselineCommands() []string {
	if len(s.BaselineCommands) == 0 {
		return DefaultBaselineCommands
	}
	return s.BaselineCommands
}

// DefaultCommandTimeout 默认命令超时秒数。
const DefaultCommandTimeout = 60

// GetCommandTimeout 获取命令超时，未配置时返回默认值。
func (s SecurityConfig) GetCommandTimeout() int {
	if s.CommandTimeout <= 0 {
		return DefaultCommandTimeout
	}
	return s.CommandTimeout
}

// CommandRule 命令白名单规则。
type CommandRule struct {
	Pattern     string `mapstructure:"pattern"`
	AutoApprove bool   `mapstructure:"auto_approve"`
}

// FSConfig 文件系统边界配置。
type FSConfig struct {
	AllowedRead  []string `mapstructure:"allowed_read"`
	AllowedWrite []string `mapstructure:"allowed_write"`
}

// SandboxConfig 沙箱配置。
type SandboxConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	WorkingDirRoot   string `mapstructure:"working_dir_root"`
	HotRetentionDays int    `mapstructure:"hot_retention_days"`
	ArchiveFormat    string `mapstructure:"archive_format"`
}

// AuditConfig 审计日志配置。
type AuditConfig struct {
	Path          string `mapstructure:"path"`
	RetentionDays int    `mapstructure:"retention_days"`
	Rotation      string `mapstructure:"rotation"`
}

// EvaluatorConfig Evaluator Agent 配置。
type EvaluatorConfig struct {
	Enabled                       bool     `mapstructure:"enabled"`
	Agent                         string   `mapstructure:"agent"`
	ReviewStages                  []string `mapstructure:"review_stages"`
	MaxReviewRetries              int      `mapstructure:"max_review_retries"`
	ConfidenceDivergenceThreshold float64  `mapstructure:"confidence_divergence_threshold"`
	MinPassScore                  int      `mapstructure:"min_pass_score"` // 最低通过评分，默认 7
}

// DefaultMinPassScore Evaluator 默认最低通过评分。
const DefaultMinPassScore = 7

// GetMinPassScore 获取最低通过评分，未配置时返回默认值 7。
func (e EvaluatorConfig) GetMinPassScore() int {
	if e.MinPassScore <= 0 {
		return DefaultMinPassScore
	}
	return e.MinPassScore
}

// PlannerConfig Planner Agent 配置。
type PlannerConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Agent   string `mapstructure:"agent"`
	Trigger string `mapstructure:"trigger"`
}

// MCTSConfig 双阶段 MCTS 配置。
type MCTSConfig struct {
	Locate      MCTSStageConfig   `mapstructure:"locate"`
	Fix         MCTSStageConfig   `mapstructure:"fix"`
	UCBC        float64           `mapstructure:"ucb_c"`
	RolloutExec RolloutExecConfig `mapstructure:"rollout_exec"`
}

// MCTSStageConfig 单个 MCTS 阶段的预算配置。
type MCTSStageConfig struct {
	MaxIterations       int     `mapstructure:"max_iterations"`
	MaxDepth            int     `mapstructure:"max_depth"`
	BranchingFactor     int     `mapstructure:"branching_factor"`
	ExpandTemperature   float64 `mapstructure:"expand_temperature"`
	ExpandMaxTokens     int     `mapstructure:"expand_max_tokens"`
	ScoreTemperature    float64 `mapstructure:"score_temperature"`
	ScoreMaxTokens      int     `mapstructure:"score_max_tokens"`
}

// DefaultExpandTemperature 默认 Expand LLM 温度。
const DefaultExpandTemperature = 0.3

// DefaultExpandMaxTokens 默认 Expand LLM max tokens。
const DefaultExpandMaxTokens = 1024

// DefaultScoreTemperature 默认 Score LLM 温度。
const DefaultScoreTemperature = 0.1

// DefaultScoreMaxTokens 默认 Score LLM max tokens。
const DefaultScoreMaxTokens = 512

// GetExpandTemperature 获取 expand 温度，未配置时返回默认值。
func (m MCTSStageConfig) GetExpandTemperature() float64 {
	if m.ExpandTemperature <= 0 {
		return DefaultExpandTemperature
	}
	return m.ExpandTemperature
}

// GetExpandMaxTokens 获取 expand max tokens，未配置时返回默认值。
func (m MCTSStageConfig) GetExpandMaxTokens() int {
	if m.ExpandMaxTokens <= 0 {
		return DefaultExpandMaxTokens
	}
	return m.ExpandMaxTokens
}

// GetScoreTemperature 获取 score 温度，未配置时返回默认值。
func (m MCTSStageConfig) GetScoreTemperature() float64 {
	if m.ScoreTemperature <= 0 {
		return DefaultScoreTemperature
	}
	return m.ScoreTemperature
}

// GetScoreMaxTokens 获取 score max tokens，未配置时返回默认值。
func (m MCTSStageConfig) GetScoreMaxTokens() int {
	if m.ScoreMaxTokens <= 0 {
		return DefaultScoreMaxTokens
	}
	return m.ScoreMaxTokens
}

// RolloutExecConfig Rollout 真实动作执行配置。
type RolloutExecConfig struct {
	Enabled           bool     `mapstructure:"enabled"`              // 总开关，默认 false
	EnabledLocate     []string `mapstructure:"enabled_locate"`       // 启用的 locate 动作类型
	EnabledFix        []string `mapstructure:"enabled_fix"`          // 启用的 fix 动作类型
	MaxRealSteps      int      `mapstructure:"max_real_steps"`       // 真实执行最大步数，默认 2
	PerActionTimeoutS int      `mapstructure:"per_action_timeout_s"` // 单动作超时，默认 30
}

// RetryConfig 重试配置。
type RetryConfig struct {
	MaxPerStage   int    `mapstructure:"max_per_stage"`
	FallbackAgent string `mapstructure:"fallback_agent"`
	LLMMaxRetries int    `mapstructure:"llm_max_retries"` // LLM 调用最大重试次数，默认 2
}

// DefaultLLMMaxRetries LLM 调用默认最大重试次数。
const DefaultLLMMaxRetries = 2

// GetLLMMaxRetries 获取 LLM 最大重试次数，未配置时返回默认值。
func (r RetryConfig) GetLLMMaxRetries() int {
	if r.LLMMaxRetries <= 0 {
		return DefaultLLMMaxRetries
	}
	return r.LLMMaxRetries
}

// EvalsConfig 离线评测配置。
type EvalsConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	Source       string `mapstructure:"source"`
	GitLabFilter string `mapstructure:"gitlab_filter"`
	JiraFilter   string `mapstructure:"jira_filter"`
	OutputDir    string `mapstructure:"output_dir"`
}

// PipelineConfig 流水线阶段配置。
type PipelineConfig struct {
	Stages PipelineStagesConfig `mapstructure:"stages"`
}

// PipelineStagesConfig 各阶段的 skill 列表等配置。
type PipelineStagesConfig struct {
	Get    PipelineStageConfig `mapstructure:"get"`
	Locate LocateStageConfig   `mapstructure:"locate"`
	Verify PipelineStageConfig `mapstructure:"verify"`
	Commit PipelineStageConfig `mapstructure:"commit"`
}

// PipelineStageConfig 单个阶段的 skill 配置。
type PipelineStageConfig struct {
	Skills []string `mapstructure:"skills"`
}

// LocateStageConfig locate 阶段的专属配置。
type LocateStageConfig struct {
	Skills          []string `mapstructure:"skills"`
	MaxSurveyRounds int      `mapstructure:"max_survey_rounds"`
}

// DefaultLocateMaxSurveyRounds locate 阶段默认最大调查轮数。
const DefaultLocateMaxSurveyRounds = 3

// GetMaxSurveyRounds 获取 locate 最大调查轮数，未配置时返回默认值。
func (c LocateStageConfig) GetMaxSurveyRounds() int {
	if c.MaxSurveyRounds <= 0 {
		return DefaultLocateMaxSurveyRounds
	}
	return c.MaxSurveyRounds
}

// Load 从指定路径加载配置文件，展开环境变量，并执行校验。
func Load(path string) (*Config, error) {
	v := viper.New()

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("$HOME/.ainspection")
		v.AddConfigPath(".")
	}

	// 环境变量绑定：config.yaml 中的 ${VAR} 通过 viper 自动展开
	v.SetEnvPrefix("")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 展开路径中的 ~ 和环境变量
	cfg.expandPaths()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// MustLoad 加载配置，失败时 panic（仅用于测试）。
func MustLoad(path string) *Config {
	cfg, err := Load(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// Validate 校验必填字段和配置合法性。
func (c *Config) Validate() error {
	// agents 必须配置（至少有 claude）
	if len(c.Agents) == 0 {
		return fmt.Errorf("agents 不能为空，至少配置一个 agent")
	}

	for name, agent := range c.Agents {
		// type 优先，兼容旧版 adapter 字段
		agentType := agent.ResolveType()
		if agentType == "" {
			return fmt.Errorf("agents.%s.type 为必填项 (可选值: claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native)", name)
		}
		if !validTypes[agentType] {
			return fmt.Errorf("agents.%s.type=%s 无效，可选值: claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native", name, agentType)
		}
		// llm_native 必须有 endpoint 和 api_key
		if agentType == "llm_native" {
			if agent.Endpoint == "" {
				return fmt.Errorf("agents.%s.endpoint 为必填项 (llm_native)", name)
			}
			if agent.APIKey == "" && agent.APIKeyEnv == "" {
				return fmt.Errorf("agents.%s.api_key 或 api_key_env 为必填项 (llm_native)", name)
			}
		}
		if agent.Model == "" {
			return fmt.Errorf("agents.%s.model 为必填项", name)
		}
	}

	// services 不能为空
	if len(c.Services) == 0 {
		return fmt.Errorf("services 不能为空")
	}

	for i, svc := range c.Services {
		if svc.Name == "" {
			return fmt.Errorf("services[%d].name 为必填项", i)
		}
	}

	// skills.path 必须存在
	if c.Skills.Path != "" {
		if _, err := os.Stat(c.expandPath(c.Skills.Path)); os.IsNotExist(err) {
			return fmt.Errorf("skills.path %s 不存在", c.Skills.Path)
		}
	}

	// prompts.path 必须存在
	if c.Prompts.Path != "" {
		if _, err := os.Stat(c.expandPath(c.Prompts.Path)); os.IsNotExist(err) {
			return fmt.Errorf("prompts.path %s 不存在", c.Prompts.Path)
		}
	}

	return nil
}

// expandPaths 展开配置中的 ~ 和 ${VAR} 路径。
func (c *Config) expandPaths() {
	c.DataDir = c.expandPath(c.DataDir)
	c.K3S.Kubeconfig = c.expandPath(c.K3S.Kubeconfig)
	c.Security.Sandbox.WorkingDirRoot = c.expandPath(c.Security.Sandbox.WorkingDirRoot)
	c.Security.Audit.Path = c.expandPath(c.Security.Audit.Path)
	c.Evals.OutputDir = c.expandPath(c.Evals.OutputDir)

	for i := range c.Services {
		c.Services[i].RepoPath = c.expandPath(c.Services[i].RepoPath)
	}
}

// expandPath 展开 ~ 和环境变量。
func (c *Config) expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
	}
	// ${VAR} 展开由 viper + AutomaticEnv 处理，这里处理 ~ 即可
	return os.ExpandEnv(path)
}

// GetAgentConfig 获取指定 agent 的配置（如果不存在返回 nil）。
func (c *Config) GetAgentConfig(name string) *AgentConfig {
	if agent, ok := c.Agents[name]; ok {
		return &agent
	}
	return nil
}

// GetServiceConfig 获取指定 service 的配置。
func (c *Config) GetServiceConfig(name string) *ServiceConfig {
	for i := range c.Services {
		if c.Services[i].Name == name {
			return &c.Services[i]
		}
	}
	return nil
}
