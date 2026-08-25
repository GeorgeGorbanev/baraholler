package notifier

import (
	"context"
	"fmt"
	"os/exec"
)

const macOSScript = `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`

type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

type MacOS struct {
	runner CommandRunner
}

func NewMacOS(runner CommandRunner) *MacOS {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &MacOS{runner: runner}
}

func (*MacOS) ID() string {
	return "macos"
}

func (n *MacOS) Notify(ctx context.Context, match Match) error {
	title := "Baraholler: " + match.RuleName
	body := match.SourceName + " — " + Snippet(match.Text)
	if err := n.runner.Run(ctx, "/usr/bin/osascript", "-e", macOSScript, "--", title, body); err != nil {
		return fmt.Errorf("macos notification send failed: %w", err)
	}
	return nil
}
