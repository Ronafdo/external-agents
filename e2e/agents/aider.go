package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Aider exercises the launcher-owned session workflow. It requires both
// aider-entire and the real aider CLI to be installed before lifecycle tests.
func init() {
	if env := os.Getenv("E2E_AGENT"); env != "" && env != "aider" {
		return
	}
	Register(&Aider{})
	RegisterGate("aider", 1)
}

type Aider struct{}

func (a *Aider) Name() string               { return "aider" }
func (a *Aider) Binary() string             { return "aider-entire" }
func (a *Aider) EntireAgent() string        { return "aider" }
func (a *Aider) PromptPattern() string      { return `>` }
func (a *Aider) TimeoutMultiplier() float64 { return 1.5 }
func (a *Aider) IsExternalAgent() bool      { return true }
func (a *Aider) Bootstrap() error           { _, err := exec.LookPath(a.Binary()); return err }
func (a *Aider) IsTransientError(out Output, _ error) bool {
	text := strings.ToLower(out.Stdout + out.Stderr)
	return strings.Contains(text, "rate limit") || strings.Contains(text, "429") || strings.Contains(text, "503")
}
func (a *Aider) RunPrompt(ctx context.Context, dir, prompt string, _ ...Option) (Output, error) {
	bin, err := exec.LookPath(a.Binary())
	if err != nil {
		return Output{}, fmt.Errorf("%s not in PATH: %w", a.Binary(), err)
	}
	promptFile, err := os.CreateTemp("", "aider-entire-prompt-*")
	if err != nil {
		return Output{}, fmt.Errorf("create Aider prompt file: %w", err)
	}
	promptPath := promptFile.Name()
	defer os.Remove(promptPath)
	if err := promptFile.Chmod(0600); err != nil {
		_ = promptFile.Close()
		return Output{}, fmt.Errorf("secure Aider prompt file: %w", err)
	}
	if _, err := promptFile.WriteString(prompt); err != nil {
		_ = promptFile.Close()
		return Output{}, fmt.Errorf("write Aider prompt file: %w", err)
	}
	if err := promptFile.Close(); err != nil {
		return Output{}, fmt.Errorf("close Aider prompt file: %w", err)
	}
	args := []string{"--repo", dir, "--message-file", promptPath}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}
	return Output{Command: a.Binary() + " --repo " + dir + " --message-file <redacted>", Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}, err
}
func (a *Aider) StartSession(context.Context, string) (Session, error) { return nil, nil }
