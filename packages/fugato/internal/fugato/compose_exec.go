package fugato

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

// RunCommand executes one policy-approved argv in the selected running
// service container. The in-container timeout bounds the workload even if the Docker
// client is cancelled. This first adapter uses the image's `timeout` command.
func (ComposeBackend) RunCommand(ctx context.Context, p Project, input control.CommandRequest) (control.CommandResult, error) {
	if input.RuntimeID != p.ID {
		return control.CommandResult{}, errors.New("runtime identity mismatch")
	}
	if input.Dir != "" {
		return control.CommandResult{}, errors.New("relative command directories are not supported by the Compose adapter")
	}
	if len(input.Argv) == 0 || strings.HasPrefix(input.Argv[0], "-") {
		return control.CommandResult{}, errors.New("invalid command executable")
	}
	var err error
	p, err = (ComposeBackend{}).Prepare(ctx, p)
	if err != nil {
		return control.CommandResult{}, err
	}
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return control.CommandResult{}, err
	}
	selected, err := selectedServices(p, model)
	if err != nil {
		return control.CommandResult{}, err
	}
	if !selected[input.Service] {
		return control.CommandResult{}, errors.New("command service is not selected by this environment")
	}
	seconds := (input.TimeoutMS + 999) / 1000
	if seconds < 1 {
		seconds = 1
	}
	args := []string{"exec", "-T", input.Service, "timeout", "-s", "KILL", fmt.Sprintf("%ds", seconds)}
	args = append(args, input.Argv...)
	cmd := composeCommand(ctx, p, args...)
	stdout, stderr := &capBuffer{max: 64 << 10}, &capBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	result := control.CommandResult{Service: input.Service, Stdout: strings.ToValidUTF8(stdout.String(), "�"), Stderr: strings.ToValidUTF8(stderr.String(), "�"), Truncated: stdout.exceeded || stderr.exceeded}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if runErr == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("execute in Compose service: %w", runErr)
}

var _ CommandBackend = ComposeBackend{}
