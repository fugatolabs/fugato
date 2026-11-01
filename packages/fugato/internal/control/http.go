package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// Handler is mounted by the loopback-only daemon listener. Token is read from
// the daemon's user-only state directory, never sent to a browser.
func Handler(service Service, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "browser origins are not allowed")
			return
		}
		if r.URL.Path == "/v1/agent/prepare" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			preparer, ok := service.(PrepareService)
			if !ok {
				writeError(w, http.StatusNotImplemented, "preparation unavailable")
				return
			}
			var input PrepareRequest
			if !decode(w, r, &input) {
				return
			}
			if err := ValidatePrepare(input); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			report, err := preparer.Prepare(r.Context(), input)
			if err != nil && report.SchemaVersion > 0 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(struct {
					Error  string        `json:"error"`
					Report PrepareReport `json:"report"`
				}{"preparation failed", report})
				return
			}
			writeResult(w, report, err)
			return
		}
		if r.URL.Path == "/v1/agent/runtimes" && r.Method == http.MethodGet {
			items, err := service.List(r.Context())
			writeResult(w, items, err)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/agent/runtimes/"), "/")
		if !strings.HasPrefix(r.URL.Path, "/v1/agent/runtimes/") || len(parts) < 1 || len(parts) > 3 || ValidateID(parts[0]) != nil {
			writeError(w, http.StatusNotFound, "unknown route")
			return
		}
		id := parts[0]
		if len(parts) == 1 && r.Method == http.MethodGet {
			item, err := service.Get(r.Context(), id)
			writeResult(w, item, err)
			return
		}
		if len(parts) == 3 && parts[1] == "verifications" && parts[2] == "latest" && r.Method == http.MethodGet {
			reader, ok := service.(interface {
				LatestVerification(context.Context, string) (LatestVerification, error)
			})
			if !ok {
				writeError(w, http.StatusNotImplemented, "latest verification unavailable")
				return
			}
			item, err := reader.LatestVerification(r.Context(), id)
			writeResult(w, item, err)
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if len(parts) == 2 && parts[1] == "start" {
			item, err := service.Start(r.Context(), id)
			writeResult(w, item, err)
			return
		}
		if len(parts) == 2 && parts[1] == "stop" {
			item, err := service.Stop(r.Context(), id)
			writeResult(w, item, err)
			return
		}
		if len(parts) == 2 && parts[1] == "logs" {
			var input LogsRequest
			if !decode(w, r, &input) {
				return
			}
			input.RuntimeID = id
			if input.Lines < 1 || input.Lines > 200 {
				writeError(w, http.StatusBadRequest, "lines must be between 1 and 200")
				return
			}
			item, err := service.Logs(r.Context(), input)
			writeResult(w, item, err)
			return
		}
		if len(parts) == 2 && parts[1] == "command" {
			var input CommandRequest
			if !decode(w, r, &input) {
				return
			}
			input.RuntimeID = id
			item, err := service.RunCommand(r.Context(), input)
			writeResult(w, item, err)
			return
		}
		if len(parts) == 3 && parts[1] == "verify" {
			var input VerifyRequest
			if !decode(w, r, &input) {
				return
			}
			input.RuntimeID = id
			if err := ValidateVerify(input); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if parts[2] == "http" {
				item, err := service.VerifyHTTP(r.Context(), input)
				writeResult(w, item, err)
				return
			}
			if parts[2] == "browser" {
				item, err := service.VerifyBrowser(r.Context(), input)
				writeResult(w, item, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, "unknown route")
	})
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "body must contain one JSON object")
		return false
	}
	return true
}

func writeResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrBusy) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type Client struct {
	base      string
	token     string
	tokenFile string
	http      *http.Client
}

func NewClient(base, token string) (*Client, error) {
	client, err := newClient(base)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("API token is required")
	}
	client.token = token
	return client, nil
}

func newClient(base string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("API URL must be a loopback HTTP origin")
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("API URL must use a loopback IP and explicit port")
	}
	return &Client{base: strings.TrimSuffix(base, "/"), http: &http.Client{Timeout: 70 * time.Second}}, nil
}

// NewFileClient reloads the daemon token for each request. A long-lived MCP
// process must not retain a token from an earlier daemon state directory.
func NewFileClient(base, tokenFile string) (*Client, error) {
	client, err := newClient(base)
	if err != nil {
		return nil, err
	}
	if _, err := readTokenFile(tokenFile); err != nil {
		return nil, err
	}
	client.tokenFile = tokenFile
	return client, nil
}

func readTokenFile(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return "", errors.New("API token file must be a user-only regular file (mode 0600)")
	}
	// NOFOLLOW rejects a symlink introduced after Lstat. NONBLOCK prevents a
	// FIFO swapped into place from trapping a long-lived MCP request.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat API token file: %w", err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 {
		return "", errors.New("API token file changed or is not a user-only regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}
	token := strings.TrimSuffix(string(data), "\n")
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("API token file contains an invalid secret")
	}
	return token, nil
}

func (c *Client) call(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+endpoint, body)
	if err != nil {
		return err
	}
	token := c.token
	if c.tokenFile != "" {
		token, err = readTokenFile(c.tokenFile)
		if err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	responseLimit := int64(2 << 20)
	if endpoint == "/v1/agent/prepare" {
		// Preparation may report thousands of paths. Its core report cap is
		// 4 MiB; allow room for the failed-report envelope as well.
		responseLimit = 8 << 20
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > responseLimit {
		return fmt.Errorf("daemon API: response exceeds %d-byte limit", responseLimit)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if report, ok := output.(*PrepareReport); ok && res.StatusCode == http.StatusUnprocessableEntity {
			var blocked struct {
				Report PrepareReport `json:"report"`
			}
			if err := json.Unmarshal(data, &blocked); err != nil {
				return fmt.Errorf("decode preparation report: %w", err)
			}
			*report = blocked.Report
		}
		if failure.Error == "" {
			failure.Error = res.Status
		}
		if res.StatusCode == http.StatusUnauthorized {
			return errors.New("daemon API: unauthorized; check FUGATO_API_URL and FUGATO_API_TOKEN_FILE against the running daemon")
		}
		return fmt.Errorf("daemon API: %s", failure.Error)
	}
	return json.Unmarshal(data, output)
}

func endpoint(id, suffix string) string { return "/v1/agent/runtimes/" + id + suffix }

func (c *Client) Prepare(ctx context.Context, input PrepareRequest) (PrepareReport, error) {
	var out PrepareReport
	if err := ValidatePrepare(input); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, "/v1/agent/prepare", input, &out)
	return out, err
}

func (c *Client) List(ctx context.Context) ([]Runtime, error) {
	var out []Runtime
	err := c.call(ctx, http.MethodGet, "/v1/agent/runtimes", nil, &out)
	return out, err
}
func (c *Client) Get(ctx context.Context, id string) (Runtime, error) {
	var out Runtime
	if err := ValidateID(id); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodGet, endpoint(id, ""), nil, &out)
	return out, err
}
func (c *Client) Start(ctx context.Context, id string) (Runtime, error) {
	var out Runtime
	if err := ValidateID(id); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(id, "/start"), nil, &out)
	return out, err
}
func (c *Client) Stop(ctx context.Context, id string) (Runtime, error) {
	var out Runtime
	if err := ValidateID(id); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(id, "/stop"), nil, &out)
	return out, err
}
func (c *Client) Logs(ctx context.Context, input LogsRequest) (LogsResult, error) {
	var out LogsResult
	if err := ValidateID(input.RuntimeID); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(input.RuntimeID, "/logs"), input, &out)
	return out, err
}
func (c *Client) RunCommand(ctx context.Context, input CommandRequest) (CommandResult, error) {
	var out CommandResult
	if err := ValidateID(input.RuntimeID); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(input.RuntimeID, "/command"), input, &out)
	return out, err
}
func (c *Client) VerifyHTTP(ctx context.Context, input VerifyRequest) (VerifyResult, error) {
	var out VerifyResult
	if err := ValidateVerify(input); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(input.RuntimeID, "/verify/http"), input, &out)
	return out, err
}
func (c *Client) VerifyBrowser(ctx context.Context, input VerifyRequest) (VerifyResult, error) {
	var out VerifyResult
	if err := ValidateVerify(input); err != nil {
		return out, err
	}
	err := c.call(ctx, http.MethodPost, endpoint(input.RuntimeID, "/verify/browser"), input, &out)
	return out, err
}
