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
	TimeoutSeconds     int      `json:"timeout_seconds"`
	AllowedWorkingDirs []string `json:"allowed_working_dirs"`
	AllowedPatterns    []string `json:"allowed_patterns"`
	BlockedPatterns    []string `json:"blocked_patterns"`
}

// Default settings
var defaultSettings = Settings{
	TimeoutSeconds: 60,
	AllowedWorkingDirs: []string{
		"/Users/jjdev/Projects",
	},
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

	// Validate command against blocked patterns
	if err := t.validateNotBlocked(params.Command, settings.BlockedPatterns); err != nil {
		return "", err
	}

	// Validate command against allowed patterns
	if err := t.validateAllowed(params.Command, settings.AllowedPatterns); err != nil {
		return "", err
	}

	// Determine working directory
	workingDir := params.WorkingDir
	if workingDir == "" {
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

	// Validate working directory
	if err := t.validateWorkingDir(workingDir, settings.AllowedWorkingDirs); err != nil {
		return "", err
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

// loadSettings loads settings from agent config or uses defaults
func (t *ori_shell_executorTool) loadSettings() Settings {
	settings := defaultSettings

	// Try to load from Settings API (requires agent context)
	agentCtx := t.GetAgentContext()
	if agentCtx.AgentDir != "" {
		sm := t.Settings()
		if sm != nil {
			if timeout, err := sm.GetInt("timeout_seconds"); err == nil && timeout > 0 {
				settings.TimeoutSeconds = timeout
			}
			// Parse newline-separated pattern strings
			if allowedDirs, err := sm.GetString("allowed_working_dirs"); err == nil && allowedDirs != "" {
				settings.AllowedWorkingDirs = parseLines(allowedDirs)
			}
			if allowedPatterns, err := sm.GetString("allowed_patterns"); err == nil && allowedPatterns != "" {
				settings.AllowedPatterns = parseLines(allowedPatterns)
			}
			if blockedPatterns, err := sm.GetString("blocked_patterns"); err == nil && blockedPatterns != "" {
				settings.BlockedPatterns = parseLines(blockedPatterns)
			}
			return settings
		}
	}

	// Fallback: try to load settings directly from agent directory
	// This works even when agent context isn't properly passed via RPC
	settingsPaths := []string{
		// Standard location for plugin-test-agent
		"agents/plugin-test-agent/plugins/ori_shell_executor/settings.json",
		// Legacy location
		"agents/plugin-test-agent/ori_shell_executor_settings.json",
	}

	for _, path := range settingsPaths {
		if data, err := os.ReadFile(path); err == nil {
			var fileSettings Settings
			if err := json.Unmarshal(data, &fileSettings); err == nil {
				return fileSettings
			}
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

// validateWorkingDir checks if working directory is allowed
func (t *ori_shell_executorTool) validateWorkingDir(dir string, allowedDirs []string) error {
	// If no restrictions, allow all
	if len(allowedDirs) == 0 {
		return nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("invalid working directory: %w", err)
	}

	for _, allowed := range allowedDirs {
		absAllowed, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absDir, absAllowed) {
			return nil
		}
	}

	return fmt.Errorf("working directory '%s' not in allowed directories: %v", dir, allowedDirs)
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

// DefaultSettings returns the default configuration
func (t *ori_shell_executorTool) DefaultSettings() map[string]interface{} {
	return map[string]interface{}{
		"timeout_seconds":      60,
		"allowed_working_dirs": defaultSettings.AllowedWorkingDirs,
		"allowed_patterns":     defaultSettings.AllowedPatterns,
		"blocked_patterns":     defaultSettings.BlockedPatterns,
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
