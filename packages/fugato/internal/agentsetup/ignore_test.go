package agentsetup

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupIgnoresLocalSettingsButKeepsPortableFilesShareable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required to verify ignore behavior")
	}
	for agent, profile := range profiles {
		t.Run(agent, func(t *testing.T) {
			options := fixture(t, agent)
			write(t, filepath.Join(options.Project, ".gitignore"), "keep.tmp\n", 0600)
			git := func(args ...string) ([]byte, error) {
				t.Helper()
				return exec.Command("git", append([]string{"-C", options.Project, "-c", "core.fsmonitor=false"}, args...)...).CombinedOutput()
			}
			if output, err := git("init", "--quiet"); err != nil {
				t.Fatalf("initialize fixture: %s, %v", output, err)
			}
			result, err := Setup(options)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Files) == 0 || result.Files[0] != ".gitignore" {
				t.Fatalf("exclusions must be applied before generated settings: %v", result.Files)
			}
			for _, name := range []string{".fugato-agent/mcp-launch.sh", profile.config, "keep.tmp"} {
				if output, err := git("check-ignore", "--no-index", "--", name); err != nil {
					t.Errorf("local file %s is not ignored: %s, %v", name, output, err)
				}
			}
			for _, name := range []string{".gitignore", profile.instructions, filepath.Join(profile.skill, "fugato-workflow/SKILL.md")} {
				if output, err := git("check-ignore", "--no-index", "--", name); err == nil {
					t.Errorf("portable file %s should be shareable: %s", name, output)
				}
			}
			if !strings.HasPrefix(read(t, filepath.Join(options.Project, ".gitignore")), "keep.tmp\n") {
				t.Fatal("existing user ignore rules were changed")
			}
		})
	}
}

func TestSetupRetainsExclusionsAcrossAgents(t *testing.T) {
	options := fixture(t, "codex")
	file := filepath.Join(options.Project, ".gitignore")
	write(t, file, "# User rules\n!/.codex/config.toml\n", 0600)
	if _, err := Setup(options); err != nil {
		t.Fatal(err)
	}
	options.Agent = "cursor"
	if _, err := Setup(options); err != nil {
		t.Fatal(err)
	}
	ignore := read(t, file)
	for _, name := range []string{"/.codex/config.toml", "/.cursor/mcp.json", "/.fugato-agent/"} {
		if !strings.Contains(ignore, "\n"+name+"\n") {
			t.Fatalf("lost local exclusion %s: %s", name, ignore)
		}
	}
	if !strings.HasPrefix(ignore, "# User rules\n!/.codex/config.toml\n") || strings.Count(ignore, ignoreStart) != 1 {
		t.Fatalf("user rules or managed block changed unexpectedly: %s", ignore)
	}
	if strings.Contains(ignore, "/.mcp.json") {
		t.Fatal("setup ignored a profile that was not installed")
	}
	options.Agent = "codex"
	if result, err := Setup(options); err != nil || len(result.Files) != 0 {
		t.Fatalf("repeat setup changed files: %+v, %v", result, err)
	}
}
