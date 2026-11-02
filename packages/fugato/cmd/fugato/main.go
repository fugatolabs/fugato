package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
	"github.com/fugatolabs/fugato/packages/fugato/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fugato:", err)
		var status commandExitError
		if errors.As(err, &status) {
			os.Exit(status.code)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	return runTo(args, os.Stdout)
}

func runTo(args []string, output io.Writer) error {
	if len(args) == 0 {
		return printHelp(output, "")
	}
	if args[0] == "help" {
		if len(args) > 2 {
			return errors.New("usage: fugato help [command]")
		}
		if len(args) == 2 {
			return printHelp(output, args[1])
		}
		return printHelp(output, "")
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return errors.New("usage: fugato --help")
		}
		return printHelp(output, "")
	}
	if args[0] == "version" || args[0] == "--version" {
		if len(args) != 1 {
			return errors.New("usage: fugato version | fugato --version")
		}
		_, err := fmt.Fprintf(output, "fugato %s (commit %s)\n", version.Version, version.Commit)
		return err
	}
	if !knownCommand(args[0]) {
		return fmt.Errorf("unknown command %q; run fugato --help", args[0])
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" {
			return printHelp(output, args[0])
		}
	}
	if args[0] == "schema" || args[0] == "example" {
		return printReference(output, args[0], args[1:])
	}
	if args[0] == "setup" {
		return setupAgent(args[1:], output)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "destroy":
		return destroyRuntime(ctx, args[1:], output)
	case "prepare":
		return prepareWorktree(ctx, args[1:], output)
	case "projects", "worktrees":
		return environmentReadOnly(ctx, args[0], args[1:], output)
	case "run", "check":
		return environmentCommand(ctx, args[0], args[1:], output)
	case "open":
		return openEnvironment(ctx, args[1:], output)
	case "doctor":
		path, jsonOutput, err := doctorArgs(args[1:])
		if err != nil {
			return err
		}
		report := fugato.Doctor(ctx, path)
		if jsonOutput {
			if err := json.NewEncoder(output).Encode(newDoctorJSONReport(report)); err != nil {
				return err
			}
			if !report.OK() {
				return errors.New("doctor found blocking project errors")
			}
			return nil
		}
		if report.Project.ID != "" {
			p := report.Project
			fmt.Fprintf(output, "Project: %s\nRuntime ID: %s\nCompose (%s): %s\n", p.Dir, p.ID, report.Sources["compose"], strings.Join(p.ComposeFiles, ", "))
			if p.Config.Browser.Service == "" {
				fmt.Fprintln(output, "Browser: unconfigured")
			} else {
				fmt.Fprintf(output, "Browser: %s  profile: %s\n", p.Config.Browser.Service, p.Config.Browser.Profile)
			}
			if report.PlanAvailable {
				printStartupPlan(output, report.Plan)
			} else {
				fmt.Fprintln(output, "Startup plan unavailable; see findings below.")
			}
		}
		for _, finding := range report.Findings {
			fmt.Fprintf(output, "%s [%s] %s: %s\n", finding.Severity, finding.Code, finding.Subject, finding.Message)
		}
		if !report.OK() {
			return errors.New("doctor found blocking project errors")
		}
		return nil
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		path := flags.String("worktree", ".", "path inside the Git worktree project")
		compose := flags.String("compose", "", "nonstandard Compose file")
		var services repeatedFlag
		flags.Var(&services, "service", "Compose startup root (repeatable)")
		browserService := flags.String("browser-service", "", "optional browser service")
		browserProfile := flags.String("browser-profile", "", "browser Compose profile")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) != 0 {
			return fmt.Errorf("unexpected arguments: %v", flags.Args())
		}
		p, wrote, err := fugato.Init(ctx, *path, fugato.InitChoices{Compose: *compose, Services: services, BrowserService: *browserService, BrowserProfile: *browserProfile})
		if err != nil {
			return err
		}
		if wrote {
			fmt.Fprintf(output, "Wrote %s\n", p.ConfigFile)
		} else {
			fmt.Fprintf(output, "No .fugato.yaml needed for %s\n", p.Dir)
		}
		return nil
	}
	var upProject fugato.Project
	var upPlan fugato.StartupPlan
	if args[0] == "up" || args[0] == "create" {
		var err error
		upProject, err = projectArg(ctx, args[0], args[1:])
		if err != nil {
			return err
		}
		if args[0] == "up" {
			upPlan, err = fugato.ProjectStartupPlan(ctx, upProject)
			if err != nil {
				return fmt.Errorf("inspect startup plan: %w", err)
			}
		}
	}
	settings, err := fugato.DefaultSettings()
	if err != nil {
		return err
	}
	if args[0] == "daemon" {
		return fugato.RunDaemon(ctx, settings)
	}
	client := localClient{settings: settings}
	if err := client.ensureDaemon(ctx); err != nil {
		return err
	}
	switch args[0] {
	case "create":
		var result fugato.Runtime
		body := map[string]string{"path": upProject.Dir, "expected_runtime_id": upProject.ID}
		if err := client.request(ctx, http.MethodPost, "/v1/runtimes/create", body, &result); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "%s  %s\n", result.ID, result.Observed)
		return err
	case "up":
		var result fugato.Runtime
		if err := client.request(ctx, http.MethodPost, "/v1/runtimes", map[string]string{"path": upProject.Dir}, &result); err != nil {
			return err
		}
		fmt.Fprintf(output, "%s  %s\n", result.ID, result.Observed)
		printStartupPlan(output, upPlan)
		if len(result.Endpoints) > 0 {
			for _, binding := range result.Endpoints {
				if binding.URL != "" {
					fmt.Fprintf(output, "%s  %s\n", binding.Name, binding.URL)
				}
			}
		} else if result.PreviewURL != "" {
			fmt.Fprintln(output, result.PreviewURL)
		}
		return nil
	case "ls":
		if len(args) != 1 {
			return errors.New("usage: fugato ls")
		}
		var items []fugato.Runtime
		if err := client.request(ctx, http.MethodGet, "/v1/runtimes", nil, &items); err != nil {
			return err
		}
		return printRuntimes(ctx, output, items)
	case "open", "logs", "ports", "down":
		project, err := projectArg(ctx, args[0], args[1:])
		if err != nil {
			return err
		}
		base := "/v1/runtimes/" + project.ID
		switch args[0] {
		case "open":
			var result fugato.Runtime
			if err := client.request(ctx, http.MethodGet, base, nil, &result); err != nil {
				return err
			}
			if result.PreviewURL == "" {
				return errors.New("runtime has no healthy preview; run fugato up")
			}
			fmt.Println(result.PreviewURL)
			command := "open"
			if runtime.GOOS == "linux" {
				command = "xdg-open"
			}
			return exec.CommandContext(ctx, command, result.PreviewURL).Run()
		case "logs":
			follow := false
			for _, arg := range args[1:] {
				if arg == "--follow" {
					follow = true
				}
			}
			path := base + "/logs"
			if follow {
				path += "?follow=1"
			}
			return client.stream(ctx, path, os.Stdout)
		case "ports":
			var bindings []fugato.PortBinding
			if err := client.request(ctx, http.MethodGet, base+"/ports", nil, &bindings); err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TARGET\tPORT\tPROTOCOL\tHOST")
			for _, binding := range bindings {
				target := binding.Service
				port := fmt.Sprint(binding.ContainerPort)
				if target == "" {
					target = binding.Command
					port = "-"
				}
				if target == "" {
					target = binding.Name
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", target, port, binding.Protocol, fmt.Sprintf("%s:%d", binding.HostIP, binding.HostPort))
			}
			return w.Flush()
		case "down":
			var result fugato.Runtime
			if err := client.request(ctx, http.MethodPost, base+"/down", nil, &result); err != nil {
				return err
			}
			fmt.Printf("%s  %s (data retained)\n", result.ID, result.Observed)
			return nil
		}
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func worktreeArg(command string, args []string) (string, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("worktree", ".", "path inside a Git worktree project")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if len(flags.Args()) != 0 {
		return "", fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return *path, nil
}

func projectArg(ctx context.Context, command string, args []string) (fugato.Project, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("worktree", ".", "path to a Git worktree project")
	if command == "logs" {
		flags.Bool("follow", false, "follow new log lines")
	}
	if err := flags.Parse(args); err != nil {
		return fugato.Project{}, err
	}
	for _, arg := range flags.Args() {
		if command == "logs" && arg == "--follow" {
			continue
		}
		return fugato.Project{}, fmt.Errorf("unexpected argument %q", arg)
	}
	return fugato.ResolveProject(ctx, *path)
}

type localClient struct{ settings fugato.Settings }

var localHTTPTransport = &http.Transport{
	Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ResponseHeaderTimeout: 6 * time.Minute, IdleConnTimeout: time.Minute,
	MaxIdleConns: 4, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 8,
}

func (c localClient) token() (string, error) {
	data, err := os.ReadFile(filepath.Join(c.settings.StateDir, "token"))
	return strings.TrimSpace(string(data)), err
}

func (c localClient) ensureDaemon(ctx context.Context) error {
	if token, err := c.token(); err == nil && c.healthy(ctx, token) {
		return nil
	}
	if err := os.MkdirAll(c.settings.StateDir, 0700); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(c.settings.StateDir, "daemon.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(executable, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if token, err := c.token(); err == nil && c.healthy(ctx, token) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("daemon did not start; inspect %s", filepath.Join(c.settings.StateDir, "daemon.log"))
}

func (c localClient) healthy(ctx context.Context, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.settings.APIAddr+"/v1/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Timeout: 500 * time.Millisecond, Transport: localHTTPTransport}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func (c localClient) request(ctx context.Context, method, path string, input any, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&problem)
		if problem.Error == "" {
			problem.Error = response.Status
		}
		return errors.New(problem.Error)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func (c localClient) stream(ctx context.Context, path string, output io.Writer) error {
	if !strings.HasSuffix(path, "?follow=1") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Minute)
		defer cancel()
	}
	response, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return fmt.Errorf("log request returned HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(output, response.Body)
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	if err != nil {
		return err
	}
	if response.Trailer.Get(fugato.LogStatusTrailer) != "ok" {
		return errors.New("log stream failed or ended without a completion status")
	}
	return nil
}

func (c localClient) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.settings.APIAddr+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Transport: localHTTPTransport}
	return client.Do(req)
}

type repeatedFlag []string

func (f *repeatedFlag) String() string         { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error { *f = append(*f, value); return nil }
