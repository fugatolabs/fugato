package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

// Target is resolved from the daemon registry. BrowserURL is reachable only
// inside the selected runtime; HTTPURL is the daemon's loopback upstream.
type Target struct {
	Runtime    control.Runtime
	ProjectDir string
	HTTPURL    string
	BrowserURL string
}

type BrowserEvidence struct {
	Status     int                    `json:"status"`
	Issues     []control.BrowserIssue `json:"issues"`
	Screenshot []byte                 `json:"-"`
}

// BrowserRunner executes the bundled Playwright worker in the selected
// environment and returns its screenshot bytes through an adapter-owned path.
// A shared Compose adapter must use its browser service; a VM adapter uses a
// guest-local worker. No host browser or Docker socket is passed to the app.
type BrowserRunner interface {
	Run(context.Context, Target, string) (BrowserEvidence, error)
}

type Verifier struct {
	Runner       BrowserRunner
	ArtifactsDir string
	HTTPClient   *http.Client
}

func checkedURL(base, requestPath string) (string, error) {
	if requestPath == "" {
		requestPath = "/"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("runtime endpoint is invalid")
	}
	rel, err := url.Parse(requestPath)
	if err != nil || rel.IsAbs() || rel.Host != "" {
		return "", errors.New("invalid verification path")
	}
	return u.ResolveReference(rel).String(), nil
}

func timeout(input control.VerifyRequest) time.Duration {
	if input.TimeoutMS == 0 {
		return 30 * time.Second
	}
	return time.Duration(input.TimeoutMS) * time.Millisecond
}

func (v Verifier) HTTP(ctx context.Context, target Target, input control.VerifyRequest) (control.VerifyResult, error) {
	if err := control.ValidateVerify(input); err != nil {
		return control.VerifyResult{}, err
	}
	if target.Runtime.ID != input.RuntimeID {
		return control.VerifyResult{}, errors.New("runtime identity mismatch")
	}
	u, err := checkedURL(target.HTTPURL, input.Path)
	if err != nil {
		return control.VerifyResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(input))
	defer cancel()
	start := time.Now()
	result := control.VerifyResult{Runtime: target.Runtime, URL: u, Issues: []control.BrowserIssue{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return result, err
	}
	client := v.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := copyClient.Do(req)
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Issues = append(result.Issues, control.BrowserIssue{Kind: "network", URL: u, Text: err.Error()})
		return result, nil
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
	result.Status = res.StatusCode
	result.OK = res.StatusCode >= 200 && res.StatusCode < 300
	if !result.OK {
		result.Issues = append(result.Issues, control.BrowserIssue{Kind: "http", URL: u, Text: fmt.Sprintf("HTTP %d", res.StatusCode)})
	}
	return result, nil
}

func (v Verifier) Browser(ctx context.Context, target Target, input control.VerifyRequest) (control.VerifyResult, error) {
	if err := control.ValidateVerify(input); err != nil {
		return control.VerifyResult{}, err
	}
	if target.Runtime.ID != input.RuntimeID {
		return control.VerifyResult{}, errors.New("runtime identity mismatch")
	}
	if v.Runner == nil {
		return control.VerifyResult{}, errors.New("browser runner unavailable")
	}
	u, err := checkedURL(target.BrowserURL, input.Path)
	if err != nil {
		return control.VerifyResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(input))
	defer cancel()
	start := time.Now()
	evidence, runErr := v.Runner.Run(ctx, target, u)
	result := control.VerifyResult{Runtime: target.Runtime, URL: u, Status: evidence.Status, DurationMS: time.Since(start).Milliseconds(), Issues: evidence.Issues}
	if result.Issues == nil {
		result.Issues = []control.BrowserIssue{}
	}
	if runErr != nil {
		result.Issues = append(result.Issues, control.BrowserIssue{Kind: "browser", Text: runErr.Error()})
	}
	if len(evidence.Screenshot) > 0 {
		if len(evidence.Screenshot) > 8<<20 {
			return result, errors.New("browser screenshot exceeds 8 MiB")
		}
		if v.ArtifactsDir == "" {
			return result, errors.New("artifact directory is not configured")
		}
		dir := filepath.Join(v.ArtifactsDir, target.Runtime.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return result, err
		}
		file, err := os.CreateTemp(dir, "verify-*.png")
		if err != nil {
			return result, err
		}
		name := file.Name()
		if err := file.Chmod(0600); err != nil {
			file.Close()
			os.Remove(name)
			return result, err
		}
		if _, err := file.Write(evidence.Screenshot); err != nil {
			file.Close()
			os.Remove(name)
			return result, err
		}
		if err := file.Close(); err != nil {
			os.Remove(name)
			return result, err
		}
		result.ScreenshotPath = name
	}
	result.OK = runErr == nil && evidence.Status >= 200 && evidence.Status < 400 && len(result.Issues) == 0
	return result, nil
}

// RedactLogs bounds logs included with a failed report. Adapters should
// additionally redact secrets before supplying log text.
func RedactLogs(logs string) string {
	const max = 16 << 10
	if len(logs) > max {
		logs = logs[len(logs)-max:]
	}
	return strings.ToValidUTF8(logs, "�")
}
