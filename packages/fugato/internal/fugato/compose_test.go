package fugato

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateComposeModelRejectsSharedResources(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	p := Project{Dir: dir, ComposeProject: "fugato-1234567890abcdef"}
	p.Config.Start.Services = []string{"web"}
	p.Config.Endpoints = map[string]EndpointConfig{"site": {Service: "web", Port: 3000, Protocol: "http", Browser: true}}
	data := fmt.Sprintf(`{
		"services": {
			"web": {"build": {"context": %q}, "depends_on": {"db": {"condition": "service_healthy"}}, "ports": [{"host_ip": "127.0.0.1", "target": 3000, "protocol": "tcp"}], "volumes": [{"type": "bind", "source": %q}]},
			"db": {"volumes": [{"type": "volume", "source": "postgres_data"}]}
		},
		"volumes": {"postgres_data": {"name": "fugato-1234567890abcdef_postgres_data"}},
		"networks": {"default": {"name": "fugato-1234567890abcdef_default"}}
	}`, dir, filepath.Join(dir, "src"))
	var valid composeModel
	if err := json.Unmarshal([]byte(data), &valid); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeModel(p, valid); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
	for name, mutate := range map[string]func(*composeModel){
		"external volume": func(m *composeModel) {
			volume := m.Volumes["postgres_data"]
			volume.External = true
			m.Volumes["postgres_data"] = volume
		},
		"fixed host port": func(m *composeModel) {
			web := m.Services["web"]
			web.Ports[0].Published = json.RawMessage(`"3000"`)
			m.Services["web"] = web
		},
		"outside mount": func(m *composeModel) {
			web := m.Services["web"]
			web.Volumes[0].Source = os.TempDir()
			m.Services["web"] = web
		},
		"privileged service": func(m *composeModel) {
			web := m.Services["web"]
			web.Privileged = true
			m.Services["web"] = web
		},
		"host networking": func(m *composeModel) {
			web := m.Services["web"]
			web.NetworkMode = "host"
			m.Services["web"] = web
		},
		"outside Dockerfile": func(m *composeModel) {
			web := m.Services["web"]
			web.Build.Dockerfile = filepath.Join(os.TempDir(), "outside.Dockerfile")
			m.Services["web"] = web
		},
		"secret mount": func(m *composeModel) {
			web := m.Services["web"]
			web.Secrets = json.RawMessage(`[{"source":"key"}]`)
			m.Services["web"] = web
		},
	} {
		t.Run(name, func(t *testing.T) {
			var model composeModel
			if err := json.Unmarshal([]byte(data), &model); err != nil {
				t.Fatal(err)
			}
			mutate(&model)
			if err := validateComposeModel(p, model); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatal("unsafe Compose model was accepted")
			}
		})
	}
}

func TestComposePortOverrideRemapsSelectedServicesOnly(t *testing.T) {
	p := Project{}
	p.Config.Start.Services = []string{"web"}
	p.Config.Endpoints = map[string]EndpointConfig{"site": {Service: "web", Port: 3000, Protocol: "http", Browser: true}}
	model := composeModel{Services: map[string]composeService{}}
	web := composeService{DependsOn: map[string]json.RawMessage{"db": json.RawMessage(`{}`)}, Ports: []composePort{{Target: 3000, Protocol: "tcp", Published: json.RawMessage(`"3000"`)}}}
	db := composeService{Ports: []composePort{{Target: 5432, Protocol: "tcp", Published: json.RawMessage(`"5432"`)}, {Target: 5433, Protocol: "udp", Published: json.RawMessage(`"5433"`)}}}
	inactive := composeService{Profiles: []string{"debug"}, Ports: []composePort{{Target: 9000, Protocol: "tcp", Published: json.RawMessage(`"9000"`)}}}
	model.Services["web"], model.Services["db"], model.Services["inactive"] = web, db, inactive
	override, err := composePortOverride(p, model)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"web:", "db:", "target: 3000", "target: 5432", "target: 5433", "protocol: udp", "host_ip: 127.0.0.1"} {
		if !strings.Contains(override, expected) {
			t.Fatalf("override missing %q: %s", expected, override)
		}
	}
	if strings.Contains(override, "inactive:") || strings.Contains(override, "published:") {
		t.Fatalf("override included an inactive service or fixed host port: %s", override)
	}
}
