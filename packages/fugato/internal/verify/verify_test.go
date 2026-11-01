package verify

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type fakeBrowser struct{}

func (fakeBrowser) Run(context.Context, Target, string) (BrowserEvidence, error) {
	return BrowserEvidence{Status: 200, Screenshot: []byte("png"), Issues: []control.BrowserIssue{{Kind: "page", Text: "boom"}}}, nil
}

func TestHTTPReportsFailureAndDoesNotFollowRedirect(t *testing.T) {
	id := "0123456789abcdef"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://example.com"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	result, err := (Verifier{HTTPClient: client}).HTTP(context.Background(), Target{Runtime: control.Runtime{ID: id}, HTTPURL: "http://127.0.0.1:3000"}, control.VerifyRequest{RuntimeID: id, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusFound || result.OK || len(result.Issues) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBrowserKeepsEvidenceOnPageError(t *testing.T) {
	id := "0123456789abcdef"
	result, err := (Verifier{Runner: fakeBrowser{}, ArtifactsDir: t.TempDir()}).Browser(context.Background(), Target{Runtime: control.Runtime{ID: id}, BrowserURL: "http://web:3000"}, control.VerifyRequest{RuntimeID: id, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || len(result.Issues) != 1 || result.ScreenshotPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	info, err := os.Stat(result.ScreenshotPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("screenshot permissions: %v, %v", info, err)
	}
}
