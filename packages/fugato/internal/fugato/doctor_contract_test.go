package fugato

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Doctor needs a running Docker Engine. The Compose-only contract tests run
// without one; CI and the Linux VM exercise this table with Engine available.
func TestDoctorContractFindings(t *testing.T) {
	if output, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("Docker Engine is unavailable: %v: %s", err, output)
	}
	outside := t.TempDir()
	for _, tc := range []struct {
		name            string
		compose         string
		config          string
		wantOK          bool
		wantCodes       []string
		wantAbsentCodes []string
	}{
		{
			name:      "loopback source port",
			compose:   "services:\n  web:\n    image: busybox:latest\n    ports: ['127.0.0.1::3000']\n",
			wantOK:    true,
			wantCodes: []string{"PORT_REMAP", "RESOURCE_LIMIT_UNSET", "PROJECT_READY"},
		},
		{
			name:            "direct Compose resource limits",
			compose:         "services:\n  web:\n    image: busybox:latest\n    ports: ['127.0.0.1::3000']\n    mem_limit: 512m\n    cpus: 1.5\n",
			wantOK:          true,
			wantCodes:       []string{"PROJECT_READY"},
			wantAbsentCodes: []string{"RESOURCE_LIMIT_UNSET"},
		},
		{
			name:            "deploy resource limits",
			compose:         "services:\n  web:\n    image: busybox:latest\n    ports: ['127.0.0.1::3000']\n    deploy:\n      resources:\n        limits:\n          cpus: '1.5'\n          memory: 512M\n",
			wantOK:          true,
			wantCodes:       []string{"PROJECT_READY"},
			wantAbsentCodes: []string{"RESOURCE_LIMIT_UNSET"},
		},
		{
			name:      "fixed web and database ports",
			compose:   "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n    depends_on: [db]\n  db:\n    image: busybox:latest\n    ports: ['5432:5432']\n",
			config:    "version: 1\nstart: {services: [web]}\n",
			wantOK:    true,
			wantCodes: []string{"PORT_REMAP", "PORT_LAN_BINDING_CHANGED", "PROJECT_READY"},
		},
		{
			name:      "multiple services without config",
			wantOK:    true,
			compose:   "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n  db:\n    image: busybox:latest\n    ports: ['5432:5432']\n",
			wantCodes: []string{"PROJECT_READY"},
		},
		{
			name:      "missing endpoint service",
			compose:   "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n",
			config:    "version: 1\nstart: {services: [web]}\nendpoints: {site: {service: missing, port: 4000, protocol: http}}\n",
			wantCodes: []string{"PROJECT_INVALID"},
		},
		{
			name:      "host networking",
			compose:   "services:\n  web:\n    image: busybox:latest\n    network_mode: host\n    expose: ['3000']\n",
			wantCodes: []string{"COMPOSE_HOST_NETWORK"},
		},
		{
			name:      "external volume",
			compose:   "services:\n  web:\n    image: busybox:latest\n    expose: ['3000']\n    volumes: ['shared:/data']\nvolumes:\n  shared:\n    external: true\n",
			wantCodes: []string{"COMPOSE_EXTERNAL_VOLUME"},
		},
		{
			name:      "duplicate target",
			compose:   "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000', '3001:3000']\n",
			config:    "version: 1\nstart: {services: [web]}\n",
			wantCodes: []string{"PORT_DUPLICATE"},
		},
		{
			name:      "outside worktree bind",
			compose:   fmt.Sprintf("services:\n  web:\n    image: busybox:latest\n    expose: ['3000']\n    volumes: [%q]\n", outside+":/data"),
			wantCodes: []string{"COMPOSE_OUTSIDE_WORKTREE"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if output, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(tc.compose), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report := Doctor(context.Background(), dir)
			if report.OK() != tc.wantOK {
				t.Fatalf("doctor success mismatch: %+v", report.Findings)
			}
			for _, code := range tc.wantCodes {
				found := false
				for _, finding := range report.Findings {
					if finding.Code == code {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing finding %s: %+v", code, report.Findings)
				}
			}
			for _, code := range tc.wantAbsentCodes {
				for _, finding := range report.Findings {
					if finding.Code == code {
						t.Fatalf("unexpected finding %s: %+v", code, report.Findings)
					}
				}
			}
		})
	}
}
