// The Bash built-in (File 08 §8.1.3 / §8.4.3): runs a shell command under the
// sandbox's command classifier (allow/deny by risk class) and exec's it as a
// process group so a context cancel kills the whole tree, not just the parent
// (File 08 §8.4.7 "no orphaned child after exit"). Critical-risk commands
// are refused without spawning (§8.5.1). Write/Patch delegate to the Patch
// Engine (File 10) in a later sprint; Bash is the process-spawning tool here.

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/yolo-code/yolo/internal/event"
)

// NewBash returns a Bash tool confined to s. The sandbox supplies both the
// command classifier (Classify) and the working directory for the shell.
func NewBash(s *Sandbox) *Bash {
	return &Bash{sandbox: s}
}

// Bash runs a classified shell command as a process group.
type Bash struct {
	sandbox *Sandbox
}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{Exec: true},
		Cost:        CostMedium,
		Category:    "shell",
		Description: "run a classified shell command under the sandbox",
	}
}

func (b *Bash) Schema() Schema {
	return Schema{Type: "object", Required: []string{"command"}}
}

func (b *Bash) Risk(call ToolCall) event.Risk {
	// The command itself drives the risk (File 08 §8.4.3). If the args don't
	// parse, fall back to medium so the HITL gate prompts rather than running
	// an unvetted command blind.
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(call.Args, &args) != nil {
		return RiskMedium
	}
	return b.sandbox.Classify(args.Command)
}

func (b *Bash) Run(ctx context.Context, in ToolInput) (ToolOutput, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return ToolOutput{}, fmt.Errorf("bash: invalid args: %w", err)
	}

	risk := b.sandbox.Classify(args.Command)
	if risk == RiskCritical {
		// Critical commands are explicitly denied (File 08 §8.5.1) — never
		// spawn them. The dispatcher's HITL gate would deny them anyway, but
		// Bash refuses directly so a direct Run (outside Dispatch) is safe too.
		return ToolOutput{ExitCode: -1}, fmt.Errorf("bash: command denied (critical risk): %s", args.Command)
	}

	name, shellArgs := shellInvocation(args.Command)
	cmd := exec.CommandContext(ctx, name, shellArgs...)
	if b.sandbox != nil {
		cmd.Dir = b.sandbox.cwd
	}
	setProcessGroup(cmd) // so cancel kills the whole tree (File 08 §8.4.7)

	// Prepare a kill-on-close job (Windows) / no-op (Unix) before Start so the
	// tree can be reaped deterministically on cancel (File 08 §8.4.7).
	job, jobErr := prepareJob()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		closeJob(job)
		return ToolOutput{}, fmt.Errorf("bash: start: %w", err)
	}
	if jobErr == nil {
		_ = assignJobToProcess(job, cmd.Process.Pid) // best-effort; Unix no-op
	}

	// If the context is cancelled while the command runs, translate that into
	// a group kill (not just cmd.Process.Kill, which would orphan children),
	// then wait for the process to actually die so no child lingers.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		closeJob(job)      // Windows: close the job → tree dies. Unix: no-op.
		_ = killGroup(cmd) // Unix: signal the group. Windows: parent backstop.
		<-waitErr          // reap so no zombie; bounded by the kill taking effect
		return ToolOutput{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}, ctx.Err()
	case err := <-waitErr:
		closeJob(job)
		exit := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else {
				return ToolOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
			}
		}
		return ToolOutput{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit}, nil
	}
}

// shellInvocation returns the interpreter and its argv to run a command
// string as the host's native shell (File 08 §8.4.7 uses sh/cmd). The command
// string is passed via -c so the shell parses it (pipes, redirects,
// backgrounding all work as the model expects).
func shellInvocation(cmd string) (string, []string) {
	if isWindows() {
		return "cmd", []string{"/c", cmd}
	}
	return "sh", []string{"-c", cmd}
}
