package fugato

import "testing"

func TestPortBindingFindingsWarnWhenComposeOmitsHostIP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hostIP   string
		wantWarn bool
	}{
		{name: "implicit all interfaces", hostIP: "", wantWarn: true},
		{name: "explicit all interfaces", hostIP: "0.0.0.0", wantWarn: true},
		{name: "loopback", hostIP: "127.0.0.1", wantWarn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings := portBindingFindings("web", composePort{HostIP: tc.hostIP, Target: 3000})
			if len(findings) < 1 || findings[0].Code != "PORT_REMAP" || findings[0].Subject != "web:3000/tcp" {
				t.Fatalf("missing remap finding: %+v", findings)
			}
			warn := false
			for _, finding := range findings {
				warn = warn || finding.Code == "PORT_LAN_BINDING_CHANGED"
			}
			if warn != tc.wantWarn {
				t.Fatalf("unexpected reachability warning: %+v", findings)
			}
		})
	}
}
