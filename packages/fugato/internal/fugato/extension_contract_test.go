package fugato

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

func extensionRequest(t *testing.T, handler http.Handler, method, route string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var input bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&input).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, "http://127.0.0.1:46655"+route, &input)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestExtensionDiscoveryAndExpectedIdentity(t *testing.T) {
	main := testProject(t)
	commit := exec.Command("git", "-C", main, "-c", "user.name=Fugato Test", "-c", "user.email=test@example.invalid", "add", ".")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	commit = exec.Command("git", "-C", main, "-c", "user.name=Fugato Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	sibling := filepath.Join(t.TempDir(), "sibling")
	cmd := exec.Command("git", "-C", main, "worktree", "add", "-qb", "sibling", sibling)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, output)
	}
	sibling, err := filepath.EvalSymlinks(sibling)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &fakeBackend{}
	manager := NewManager(store, backend, 46656)
	handler := apiHandler(manager, "test-token", t.TempDir(), "127.0.0.1:46655")
	info := extensionRequest(t, handler, http.MethodGet, "/v1/info", nil)
	if info.Code != 200 || !strings.Contains(info.Body.String(), `"latest_verification":true`) {
		t.Fatalf("info: %d %s", info.Code, info.Body.String())
	}
	worktrees := extensionRequest(t, handler, http.MethodPost, "/v1/repositories/worktrees", map[string]string{"path": main})
	var listed repositoryWorktrees
	if worktrees.Code != 200 || json.Unmarshal(worktrees.Body.Bytes(), &listed) != nil {
		t.Fatalf("worktrees: %d %s", worktrees.Code, worktrees.Body.String())
	}
	if len(listed.Worktrees) != 2 || listed.Truncated || listed.Repository == "" {
		t.Fatalf("worktrees: %+v", listed)
	}
	resolved := extensionRequest(t, handler, http.MethodPost, "/v1/projects/resolve", map[string]string{"path": sibling})
	var project resolvedProject
	if resolved.Code != 200 || json.Unmarshal(resolved.Body.Bytes(), &project) != nil {
		t.Fatalf("resolve: %d %s", resolved.Code, resolved.Body.String())
	}
	if project.Registered || project.Repository != listed.Repository || project.Worktree != sibling || project.ProjectDir != sibling {
		t.Fatalf("project: %+v", project)
	}
	bad := extensionRequest(t, handler, http.MethodPost, "/v1/runtimes", map[string]string{"path": project.ProjectDir, "expected_runtime_id": "0123456789abcdef"})
	if bad.Code == 200 || backend.starts != 0 {
		t.Fatalf("identity mismatch mutated runtime: %d, starts=%d", bad.Code, backend.starts)
	}
	good := extensionRequest(t, handler, http.MethodPost, "/v1/runtimes", map[string]string{"path": project.ProjectDir, "expected_runtime_id": project.ID})
	if good.Code != 200 || backend.starts != 1 {
		t.Fatalf("start: %d %s", good.Code, good.Body.String())
	}
	typed := extensionRequest(t, handler, http.MethodGet, "/v1/agent/runtimes/"+project.ID, nil)
	var item control.Runtime
	if typed.Code != 200 || json.Unmarshal(typed.Body.Bytes(), &item) != nil || item.Repository != project.Repository || item.ProjectDir != project.ProjectDir {
		t.Fatalf("typed: %d %s", typed.Code, typed.Body.String())
	}
	manager.reports.record(project.ID, "http", time.Now(), control.VerifyResult{Runtime: item, OK: false, Status: 500})
	latest := extensionRequest(t, handler, http.MethodGet, "/v1/agent/runtimes/"+project.ID+"/verifications/latest", nil)
	var reports control.LatestVerification
	if latest.Code != 200 || json.Unmarshal(latest.Body.Bytes(), &reports) != nil || reports.HTTP == nil || reports.HTTP.Result.OK || reports.Browser != nil {
		t.Fatalf("latest verification: %d %s", latest.Code, latest.Body.String())
	}
	if _, err := (ControlService{Manager: manager}).VerifyBrowser(t.Context(), control.VerifyRequest{RuntimeID: project.ID}); err == nil {
		t.Fatal("unconfigured browser verification should fail as an operation")
	}
	if after := manager.reports.latest(project.ID); after.HTTP.ReportID != reports.HTTP.ReportID || after.Browser != nil {
		t.Fatalf("operation error overwrote evidence: %+v", after)
	}
}

func TestTypedListRetainsMissingWorktree(t *testing.T) {
	dir := testProject(t)
	healthy := testProject(t)
	commit := exec.Command("git", "-C", healthy, "-c", "user.name=Fugato Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit healthy fixture: %v: %s", err, output)
	}
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := DiscoverProject(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	r := Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository, ComposeProject: p.ComposeProject, Backend: "compose", Desired: "stopped", Observed: "stopped", LastError: "original lifecycle error"}
	if err := store.Save(t.Context(), &r); err != nil {
		t.Fatal(err)
	}
	healthyProject, err := DiscoverProject(t.Context(), healthy, "")
	if err != nil {
		t.Fatal(err)
	}
	healthyRuntime := Runtime{ID: healthyProject.ID, ProjectDir: healthyProject.Dir, Worktree: healthyProject.Worktree, Repository: healthyProject.Repository, ComposeProject: healthyProject.ComposeProject, Backend: "compose", Desired: "stopped", Observed: "stopped"}
	if err := store.Save(t.Context(), &healthyRuntime); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	service := ControlService{Manager: NewManager(store, &fakeBackend{}, 46656)}
	items, err := service.List(t.Context())
	if err != nil || len(items) != 2 {
		t.Fatalf("list missing worktree: %+v, %v", items, err)
	}
	byID := map[string]control.Runtime{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if byID[r.ID].Commit != "" || byID[r.ID].MetadataError == "" || byID[r.ID].LastError != r.LastError || byID[healthyProject.ID].Commit == "" || byID[healthyProject.ID].MetadataError != "" {
		t.Fatalf("missing metadata state: %+v", items)
	}
}

func TestLatestReportsBoundedAndShared(t *testing.T) {
	cache := newReportCache()
	id := "0123456789abcdef"
	failed := control.VerifyResult{OK: false, Issues: []control.BrowserIssue{{Kind: "http", Text: "HTTP 500"}}}
	cache.record(id, "http", time.Now(), failed)
	cache.record(id, "browser", time.Now(), control.VerifyResult{OK: true})
	latest := cache.latest(id)
	if latest.HTTP == nil || latest.Browser == nil || latest.HTTP.Result.OK || !latest.Browser.Result.OK || latest.HTTP.ReportID == latest.Browser.ReportID {
		t.Fatalf("latest: %+v", latest)
	}
	for i := range 110 {
		cache.record(fmt.Sprintf("%016x", i+1), "http", time.Now().Add(time.Duration(i)*time.Millisecond), failed)
	}
	if len(cache.entries) > maxRetainedRuntimes || cache.latest(id).HTTP != nil {
		t.Fatalf("eviction: %d entries", len(cache.entries))
	}
	// Large diagnostic fields retain a bounded summary.
	cache.record("fedcba9876543210", "http", time.Now(), control.VerifyResult{Issues: []control.BrowserIssue{{Kind: "http", Text: strings.Repeat("x", maxReportBytes)}}, Logs: strings.Repeat("y", maxReportBytes)})
	if cache.bytes > maxReportsBytes || cache.latest("fedcba9876543210").HTTP == nil {
		t.Fatalf("cache bounds: %d", cache.bytes)
	}
}

func TestVerificationResultIsAvailableToAnotherClient(t *testing.T) {
	dir := testProject(t)
	p, err := DiscoverProject(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status := http.StatusInternalServerError
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer upstream.Close()
	runtime := Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository,
		ComposeProject: p.ComposeProject, Backend: "compose", Desired: "running", Observed: "running"}
	if err := store.Save(t.Context(), &runtime); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, &fakeBackend{endpoint: strings.TrimPrefix(upstream.URL, "http://")}, 46656)
	service := ControlService{Manager: manager, Verifier: verify.Verifier{}}
	result, err := service.VerifyHTTP(t.Context(), control.VerifyRequest{RuntimeID: p.ID, Path: "/"})
	if err != nil || result.OK || result.Status != http.StatusInternalServerError {
		t.Fatalf("failed verification: %+v, %v", result, err)
	}
	handler := apiHandler(manager, "test-token", t.TempDir(), "127.0.0.1:46655")
	readLatest := func() control.LatestVerification {
		t.Helper()
		response := extensionRequest(t, handler, http.MethodGet, "/v1/agent/runtimes/"+p.ID+"/verifications/latest", nil)
		var latest control.LatestVerification
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &latest) != nil {
			t.Fatalf("read latest: %d %s", response.Code, response.Body.String())
		}
		return latest
	}
	failed := readLatest().HTTP
	if failed == nil || failed.Result.OK || failed.Result.Status != http.StatusInternalServerError || failed.Result.Runtime.ID != p.ID {
		t.Fatalf("another client did not see failed result: %+v", failed)
	}
	status = http.StatusOK
	result, err = service.VerifyHTTP(t.Context(), control.VerifyRequest{RuntimeID: p.ID, Path: "/"})
	if err != nil || !result.OK {
		t.Fatalf("passing verification: %+v, %v", result, err)
	}
	passing := readLatest().HTTP
	if passing == nil || !passing.Result.OK || passing.ReportID == failed.ReportID {
		t.Fatalf("latest result was not replaced: %+v", passing)
	}
}

func TestExtensionMetadataRejectsRelativePathAndOverload(t *testing.T) {
	manager := NewManager(nil, nil, 46656)
	if _, err := manager.RepositoryWorktrees(context.Background(), "."); err == nil {
		t.Fatal("relative path accepted")
	}
	for range cap(manager.admission.metadata.active) {
		release, err := manager.admission.metadata.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	if _, err := manager.ResolveProjectMetadata(context.Background(), "/tmp"); err != control.ErrBusy {
		t.Fatalf("overload: %v", err)
	}
}

func TestWorktreeListTruncationAndOutputLimit(t *testing.T) {
	var output strings.Builder
	for i := range maxListedWorktrees + 1 {
		fmt.Fprintf(&output, "worktree /definitely-missing-%d\nbranch refs/heads/test-%d\n\n", i, i)
	}
	listed, err := parseWorktreeList("/repo/.git", output.String())
	if err != nil || !listed.Truncated || len(listed.Worktrees) != maxListedWorktrees || !listed.Worktrees[0].Missing {
		t.Fatalf("truncated worktrees: %+v, %v", listed, err)
	}
	var bounded cappedGitOutput
	if _, err := bounded.Write(bytes.Repeat([]byte("a"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := bounded.Write([]byte("b")); err == nil {
		t.Fatal("oversized Git output accepted")
	}
}
