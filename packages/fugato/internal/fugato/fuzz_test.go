package fugato

import "testing"

func FuzzPreviewHost(f *testing.F) {
	f.Add("site.0123456789abcdef.localhost:46656")
	f.Add("site.0123456789abcdef.localhost:46655")
	f.Add("OTHER.localhost:46656")
	f.Add("site.0123456789abcdef.localhost:46656:1")
	f.Fuzz(func(t *testing.T, value string) {
		name, id, ok := parseNamedPreviewHost(value, 46656)
		if !ok {
			return
		}
		if name+"."+id+".localhost:46656" != value {
			t.Fatalf("accepted noncanonical host %q as %q", value, id)
		}
	})
}

func FuzzProjectConfig(f *testing.F) {
	f.Add([]byte("version: 1")) // Omitted choices are inferred from Compose later.
	f.Add([]byte("version: 1\ncompose: compose.yaml\nweb:\n  service: web\n  port: 3000\n  health: /health\ndatabase:\n  service: db\n"))
	f.Add([]byte("version: 1\nunknown: true\n"))
	f.Add([]byte("version: 1\n---\nversion: 2\n"))
	f.Add([]byte{0xff, 0xfe, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := parseProjectConfig(data)
		if err != nil {
			return
		}
		if cfg.Version != 1 {
			t.Fatalf("accepted invalid project configuration: %+v", cfg)
		}
		for _, name := range []string{cfg.Browser.Service, cfg.Browser.Profile} {
			if name != "" && !serviceName.MatchString(name) {
				t.Fatalf("accepted invalid service or profile name %q", name)
			}
		}
		if cfg.Browser.Profile != "" && cfg.Browser.Service == "" {
			t.Fatal("accepted browser profile without a browser service")
		}
	})
}
