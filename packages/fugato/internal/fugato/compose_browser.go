package fugato

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

// ComposeBrowserRunner runs the Playwright worker in the selected project's
// browser service. Revalidate the current Compose model before every run,
// because configuration may have changed since the runtime was started.
// No host-side browser state is shared between runs.
type ComposeBrowserRunner struct{}

type capBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > b.max {
		b.exceeded = true
	}
	if room := b.max - b.Len(); room > 0 {
		_, _ = b.Buffer.Write(p[:min(room, n)])
	}
	return n, nil
}

func (ComposeBrowserRunner) Run(ctx context.Context, target verify.Target, url string) (verify.BrowserEvidence, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return verify.BrowserEvidence{}, errors.New("browser verification requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return verify.BrowserEvidence{}, err
	}
	p, err := ResolveProject(ctx, target.ProjectDir)
	if err != nil {
		return verify.BrowserEvidence{}, fmt.Errorf("resolve browser project: %w", err)
	}
	if p.ID != target.Runtime.ID {
		return verify.BrowserEvidence{}, errors.New("worktree identity changed")
	}
	service := p.Config.Browser.Service
	if service == "" {
		return verify.BrowserEvidence{}, errors.New("browser verification is unconfigured; set browser.service in .fugato.yaml")
	}
	p, err = (ComposeBackend{}).Prepare(ctx, p)
	if err != nil {
		return verify.BrowserEvidence{}, err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return verify.BrowserEvidence{}, context.DeadlineExceeded
	}
	ms := remaining.Milliseconds()
	if ms > 60000 {
		ms = 60000
	}
	args := []string{}
	if p.Config.Browser.Profile != "" {
		args = append(args, "--profile", p.Config.Browser.Profile)
	}
	args = append(args, "run", "--no-deps", "--rm", "-T",
		"-e", "FUGATO_VERIFY_URL="+url, "-e", fmt.Sprintf("FUGATO_TIMEOUT_MS=%d", ms),
		service, "node", "-e", verify.BrowserScript)
	cmd := composeCommand(ctx, p, args...)
	stdout := &capBuffer{max: 12 << 20}
	stderr := &capBuffer{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if stdout.exceeded {
		return verify.BrowserEvidence{}, errors.New("browser result exceeded 12 MiB")
	}
	evidence, parseErr := verify.ParseWorkerOutput(bytes.TrimSpace(stdout.Bytes()))
	if parseErr != nil {
		if runErr != nil {
			return verify.BrowserEvidence{}, fmt.Errorf("browser service failed: %w: %s", runErr, stderr.String())
		}
		return verify.BrowserEvidence{}, fmt.Errorf("parse browser evidence: %w", parseErr)
	}
	if runErr != nil {
		return evidence, fmt.Errorf("browser service failed: %w", runErr)
	}
	return evidence, nil
}
