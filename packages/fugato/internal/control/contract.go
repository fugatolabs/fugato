// Package control defines the versioned local API between the daemon and thin clients.
// Runtime backends implement Service through the daemon; clients never invoke Docker.
package control

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "v1"

// PrepareService is optional because preparation precedes runtime registration.
type PrepareService interface {
	Prepare(context.Context, PrepareRequest) (PrepareReport, error)
}

// ValidatePrepare requires explicit host paths on daemon and MCP requests.
func ValidatePrepare(input PrepareRequest) error {
	if !filepath.IsAbs(input.Path) || !filepath.IsAbs(input.From) {
		return errors.New("path and from must be absolute project directory paths")
	}
	return nil
}

var runtimeID = regexp.MustCompile(`^[a-f0-9]{16}$`)

type Runtime struct {
	ID            string `json:"id"`
	Worktree      string `json:"worktree"`
	Repository    string `json:"repository"`
	ProjectDir    string `json:"project_dir"`
	Commit        string `json:"commit"`
	MetadataError string `json:"metadata_error,omitempty"`
	Backend       string `json:"backend"`
	Desired       string `json:"desired"`
	Observed      string `json:"observed"`
	PreviewURL    string `json:"preview_url,omitempty"`
	Health        string `json:"health,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type LogsRequest struct {
	RuntimeID   string `json:"runtime_id"`
	Service     string `json:"service,omitempty"`
	ServiceKind string `json:"service_kind,omitempty"`
	Lines       int    `json:"lines,omitempty"`
}

type LogsResult struct {
	Runtime     Runtime `json:"runtime"`
	Service     string  `json:"service,omitempty"`
	ServiceKind string  `json:"service_kind,omitempty"`
	Text        string  `json:"text"`
	Truncated   bool    `json:"truncated"`
}

type CommandRequest struct {
	RuntimeID string   `json:"runtime_id"`
	Service   string   `json:"service"`
	Argv      []string `json:"argv"`
	Dir       string   `json:"dir,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

type CommandResult struct {
	Runtime   Runtime `json:"runtime"`
	Service   string  `json:"service"`
	ExitCode  int     `json:"exit_code"`
	Stdout    string  `json:"stdout"`
	Stderr    string  `json:"stderr"`
	Truncated bool    `json:"truncated"`
}

type VerifyRequest struct {
	Endpoint  string `json:"endpoint,omitempty"`
	RuntimeID string `json:"runtime_id"`
	Path      string `json:"path,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type BrowserIssue struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
	Text string `json:"text"`
}

type VerifyResult struct {
	Runtime        Runtime        `json:"runtime"`
	URL            string         `json:"url"`
	OK             bool           `json:"ok"`
	Status         int            `json:"status,omitempty"`
	DurationMS     int64          `json:"duration_ms"`
	Issues         []BrowserIssue `json:"issues"`
	ScreenshotPath string         `json:"screenshot_path,omitempty"`
	Logs           string         `json:"logs,omitempty"`
}

type VerificationReport struct {
	ReportID    string       `json:"report_id"`
	Kind        string       `json:"kind"`
	StartedAt   time.Time    `json:"started_at"`
	CompletedAt time.Time    `json:"completed_at"`
	Result      VerifyResult `json:"result"`
}

type LatestVerification struct {
	HTTP    *VerificationReport `json:"http"`
	Browser *VerificationReport `json:"browser"`
}

// Service is the daemon seam. Implementations must resolve runtime_id from the
// registry and enforce ownership, execution policy, and endpoint scope.
type Service interface {
	List(context.Context) ([]Runtime, error)
	Get(context.Context, string) (Runtime, error)
	Start(context.Context, string) (Runtime, error)
	Stop(context.Context, string) (Runtime, error)
	Logs(context.Context, LogsRequest) (LogsResult, error)
	RunCommand(context.Context, CommandRequest) (CommandResult, error)
	VerifyHTTP(context.Context, VerifyRequest) (VerifyResult, error)
	VerifyBrowser(context.Context, VerifyRequest) (VerifyResult, error)
}

func ValidateID(id string) error {
	if !runtimeID.MatchString(id) {
		return errors.New("runtime_id must be a 16-character lowercase hex ID")
	}
	return nil
}

func ValidateVerify(r VerifyRequest) error {
	if r.Endpoint != "" && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`).MatchString(r.Endpoint) {
		return errors.New("invalid endpoint name")
	}
	if err := ValidateID(r.RuntimeID); err != nil {
		return err
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > int((60*time.Second)/time.Millisecond) {
		return errors.New("timeout_ms must be between 0 and 60000")
	}
	if r.Path == "" {
		return nil
	}
	u, err := url.ParseRequestURI(r.Path)
	if err != nil || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") || u.IsAbs() || u.Host != "" || u.Fragment != "" || path.Clean(u.Path) != u.Path || strings.Contains(r.Path, "\\") {
		return errors.New("path must be a clean relative URL path beginning with /")
	}
	return nil
}

// CommandPolicy defaults to deny. The daemon supplies an explicit allowlist.
type CommandPolicy struct {
	// Commands maps a service to exact allowed argv sequences. No shell or
	// executable-only wildcard is available.
	Commands map[string][][]string
}

func (p CommandPolicy) Validate(r CommandRequest) error {
	if err := ValidateID(r.RuntimeID); err != nil {
		return err
	}
	if len(r.Argv) == 0 || len(r.Argv) > 32 {
		return errors.New("invalid command length")
	}
	for _, arg := range r.Argv {
		if arg == "" || strings.ContainsRune(arg, 0) || len(arg) > 4096 {
			return errors.New("invalid command argument")
		}
	}
	if r.Dir != "" && (path.IsAbs(r.Dir) || path.Clean(r.Dir) != r.Dir || r.Dir == ".." || strings.HasPrefix(r.Dir, "../")) {
		return errors.New("dir must stay relative to the service work directory")
	}
	if r.TimeoutMS < 1 || r.TimeoutMS > 30000 {
		return errors.New("timeout_ms must be between 1 and 30000")
	}
	for _, allowed := range p.Commands[r.Service] {
		if len(allowed) != len(r.Argv) {
			continue
		}
		match := true
		for i := range allowed {
			if allowed[i] != r.Argv[i] {
				match = false
				break
			}
		}
		if match {
			return nil
		}
	}
	return fmt.Errorf("command is not allowed in service %q", r.Service)
}
