package main

//go:generate ../../ori-agent/bin/ori-plugin-gen -yaml=plugin.yaml -output=ori_shell_executor_generated.go

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/johnjallday/ori-agent/pluginapi"
)

//go:embed plugin.yaml
var configYAML string

// ori_shell_executorTool implements the PluginTool interface
// Note: Compile-time interface check is in ori_shell_executor_generated.go
type ori_shell_executorTool struct {
	pluginapi.BasePlugin
}

// Settings loaded from agent config
type Settings struct {
	TimeoutSeconds           int      `json:"timeout_seconds"`
	DefaultWorkingDir        string   `json:"default_working_dir"`
	AllowedPatterns          []string `json:"allowed_patterns"`
	BlockedPatterns          []string `json:"blocked_patterns"`
	AllowShellMetacharacters bool     `json:"allow_shell_metacharacters"`
}

// Default settings
var defaultSettings = Settings{
	TimeoutSeconds:    60,
	DefaultWorkingDir: "",
	AllowedPatterns: []string{
		"./scripts/*",
		"git *",
		"go *",
		"make *",
		"npm *",
		"ls *",
		"cat *",
		"echo *",
		"pwd",
		"which *",
		"env",
	},
	BlockedPatterns: []string{
		"rm -rf /*",
		"rm -rf ~/*",
		"sudo *",
		"> /dev/*",
		"curl * | sh",
		"curl * | bash",
		"wget * | sh",
		"wget * | bash",
		"chmod 777 *",
		":(){ :|:& };:",
		"dd if=*",
		"mkfs.*",
		"eval *",
	},
	AllowShellMetacharacters: false,
}

// Note: Definition() is inherited from BasePlugin, which automatically reads from plugin.yaml
// Note: Call() is auto-generated in ori_shell_executor_generated.go from plugin.yaml

// Execute contains the business logic - called by the generated Call() method
func (t *ori_shell_executorTool) Execute(ctx context.Context, params *OriShellExecutorParams) (string, error) {
	if params.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	// Load settings
	settings := t.loadSettings()

	// Reject shell metacharacters unless explicitly allowed
	if err := t.validateShellMetacharacters(params.Command, settings.AllowShellMetacharacters); err != nil {
		return "", err
	}

	// Validate command against blocked patterns
	if err := t.validateNotBlocked(params.Command, settings.BlockedPatterns); err != nil {
		return "", err
	}

	// Validate command against allowed patterns
	if err := t.validateAllowed(params.Command, settings.AllowedPatterns); err != nil {
		return "", err
	}

	// Determine working directory: params > settings > agent context > cwd
	workingDir := params.WorkingDir
	if workingDir == "" {
		if settings.DefaultWorkingDir != "" {
			workingDir = expandTilde(settings.DefaultWorkingDir)
		} else {
			agentCtx := t.GetAgentContext()
			workingDir = agentCtx.AgentDir
			if workingDir == "" {
				var err error
				workingDir, err = os.Getwd()
				if err != nil {
					return "", fmt.Errorf("failed to get working directory: %w", err)
				}
			}
		}
	}

	// Determine timeout
	timeout := params.TimeoutSeconds
	if timeout <= 0 {
		timeout = settings.TimeoutSeconds
	}
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 300 {
		timeout = 300
	}

	// Execute command
	result, err := t.executeCommand(ctx, params.Command, workingDir, timeout, params.Shell)
	if err != nil {
		return "", err
	}

	return result, nil
}

// parseLines splits a newline-separated string into a slice, trimming whitespace
func parseLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func parseStringList(value interface{}) []string {
	switch v := value.(type) {
	case string:
		return parseLines(v)
	case []string:
		return v
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					result = append(result, s)
				}
			}
		}
		return result
	default:
		return nil
	}
}

func parseBool(value interface{}) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed, true
		}
	case float64:
		return v != 0, true
	case int:
		return v != 0, true
	}
	return false, false
}

func parseInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(v)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func loadLegacySettings(path string) (Settings, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, false
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Settings{}, false
	}

	settings := defaultSettings

	if value, ok := raw["timeout_seconds"]; ok {
		if parsed, ok := parseInt(value); ok && parsed > 0 {
			settings.TimeoutSeconds = parsed
		}
	}
	if value, ok := raw["default_working_dir"]; ok {
		if parsed := parseStringList(value); len(parsed) > 0 {
			settings.DefaultWorkingDir = parsed[0]
		}
	}
	if value, ok := raw["allowed_patterns"]; ok {
		if parsed := parseStringList(value); len(parsed) > 0 {
			settings.AllowedPatterns = parsed
		}
	}
	if value, ok := raw["blocked_patterns"]; ok {
		if parsed := parseStringList(value); len(parsed) > 0 {
			settings.BlockedPatterns = parsed
		}
	}
	if value, ok := raw["allow_shell_metacharacters"]; ok {
		if parsed, ok := parseBool(value); ok {
			settings.AllowShellMetacharacters = parsed
		}
	}

	return settings, true
}

// loadSettings loads settings from agent config or uses defaults.
// Always reads fresh from disk to pick up configuration changes without server restart.
func (t *ori_shell_executorTool) loadSettings() Settings {
	settings := defaultSettings

	// Build list of paths to try
	var settingsPaths []string

	agentCtx := t.GetAgentContext()
	if agentCtx.AgentDir != "" {
		settingsPaths = append(settingsPaths, filepath.Join(agentCtx.AgentDir, "ori-shell-executor_settings.json"))
	}

	// Fallback paths if AgentDir is empty or file not found
	settingsPaths = append(settingsPaths,
		"agents/default/ori-shell-executor_settings.json",
		"agents/plugin-test-agent/ori-shell-executor_settings.json",
	)

	// Try each path, reading fresh from disk
	for _, path := range settingsPaths {
		if loadedSettings, ok := loadLegacySettings(path); ok {
			return loadedSettings
		}
	}

	return settings
}

// validateNotBlocked checks command against blocked patterns
func (t *ori_shell_executorTool) validateNotBlocked(command string, blockedPatterns []string) error {
	for _, pattern := range blockedPatterns {
		if matchesPattern(command, pattern) {
			return fmt.Errorf("command blocked by security policy: matches blocked pattern '%s'", pattern)
		}
	}
	return nil
}

// validateShellMetacharacters blocks common shell operators unless explicitly allowed.
func (t *ori_shell_executorTool) validateShellMetacharacters(command string, allow bool) error {
	if allow {
		return nil
	}

	if containsShellMetacharacters(command) {
		return fmt.Errorf("command contains shell metacharacters; set allow_shell_metacharacters to true to override")
	}

	return nil
}

// validateAllowed checks command against allowed patterns
func (t *ori_shell_executorTool) validateAllowed(command string, allowedPatterns []string) error {
	// If no patterns specified, allow all (after blocked check)
	if len(allowedPatterns) == 0 {
		return nil
	}

	for _, pattern := range allowedPatterns {
		if matchesPattern(command, pattern) {
			return nil
		}
	}

	return fmt.Errorf("command not in allowed list. Allowed patterns: %v", allowedPatterns)
}

// expandTilde expands ~ to the user's home directory
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// executeCommand runs the shell command with timeout
func (t *ori_shell_executorTool) executeCommand(ctx context.Context, command, workingDir string, timeoutSeconds int, shell string) (string, error) {
	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// Create command based on shell selection
	var cmd *exec.Cmd
	switch shell {
	case "powershell", "pwsh":
		// PowerShell (works on Windows, macOS, Linux if installed)
		cmd = exec.CommandContext(execCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	case "cmd":
		// Windows cmd.exe
		cmd = exec.CommandContext(execCtx, "cmd", "/C", command)
	case "bash":
		cmd = exec.CommandContext(execCtx, "bash", "-c", command)
	case "zsh":
		cmd = exec.CommandContext(execCtx, "zsh", "-c", command)
	case "sh":
		cmd = exec.CommandContext(execCtx, "sh", "-c", command)
	default:
		// Auto-detect based on OS
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "cmd", "/C", command)
		} else {
			cmd = exec.CommandContext(execCtx, "sh", "-c", command)
		}
	}
	cmd.Dir = workingDir

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	// Build result
	result := map[string]interface{}{
		"command":     command,
		"working_dir": workingDir,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   0,
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			result["error"] = fmt.Sprintf("command timed out after %d seconds", timeoutSeconds)
			result["exit_code"] = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result["exit_code"] = exitErr.ExitCode()
			result["error"] = err.Error()
		} else {
			result["error"] = err.Error()
			result["exit_code"] = -1
		}
	}

	// Return as JSON
	output, _ := json.MarshalIndent(result, "", "  ")
	return string(output), nil
}

// matchesPattern checks if command matches a glob-like pattern
func matchesPattern(command, pattern string) bool {
	// Exact match
	if command == pattern {
		return true
	}

	// Simple glob matching with *
	if strings.Contains(pattern, "*") {
		// Convert to prefix/suffix matching
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(command, prefix) {
				return true
			}
			// For patterns like "ls *", also match just "ls" (without args)
			// prefix is "ls ", so check if command == "ls" (prefix without trailing space)
			if strings.HasSuffix(prefix, " ") {
				baseCmd := strings.TrimSuffix(prefix, " ")
				if command == baseCmd {
					return true
				}
			}
		}
		if strings.HasPrefix(pattern, "*") {
			suffix := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(command, suffix) {
				return true
			}
		}
		// Pattern like "git *" matches "git status", "git commit", etc.
		parts := strings.SplitN(pattern, "*", 2)
		if len(parts) == 2 {
			if strings.HasPrefix(command, parts[0]) && strings.HasSuffix(command, parts[1]) {
				return true
			}
		}
	}

	return false
}

// containsShellMetacharacters checks for common shell operators to prevent command chaining.
func containsShellMetacharacters(command string) bool {
	if strings.Contains(command, "\n") {
		return true
	}

	operators := []string{
		"&&",
		"||",
		"|",
		";",
		"&",
		">",
		"<",
		"`",
		"$(",
	}
	for _, op := range operators {
		if strings.Contains(command, op) {
			return true
		}
	}
	return false
}

// DefaultSettings returns the default configuration
func (t *ori_shell_executorTool) DefaultSettings() map[string]interface{} {
	return map[string]interface{}{
		"timeout_seconds":            60,
		"default_working_dir":        defaultSettings.DefaultWorkingDir,
		"allowed_patterns":           defaultSettings.AllowedPatterns,
		"blocked_patterns":           defaultSettings.BlockedPatterns,
		"allow_shell_metacharacters": defaultSettings.AllowShellMetacharacters,
	}
}

// GetRequiredConfig returns configuration variables from plugin.yaml
func (t *ori_shell_executorTool) GetRequiredConfig() []pluginapi.ConfigVariable {
	return t.BasePlugin.GetConfigFromYAML()
}

// ValidateConfig checks if the provided configuration is valid
func (t *ori_shell_executorTool) ValidateConfig(config map[string]interface{}) error {
	// Basic validation - configuration is optional
	return nil
}

// InitializeWithConfig sets up the plugin with the provided configuration
func (t *ori_shell_executorTool) InitializeWithConfig(config map[string]interface{}) error {
	// Configuration is handled via Settings API, no additional initialization needed
	return nil
}

func main() {
	pluginapi.ServePlugin(&ori_shell_executorTool{}, configYAML)
}
