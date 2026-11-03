package agentsetup

import (
	"errors"
	"sort"
	"strings"
)

const ignoreStart = "# fugato-local:start"
const ignoreEnd = "# fugato-local:end"

// Keep only the generated machine-specific files local. Skills and project
// instructions remain available for deliberate sharing with teammates.
func localIgnore(prior, config string) (string, error) {
	entries := map[string]bool{"/.fugato-agent/": true, "/" + config: true}
	if strings.Contains(prior, ignoreStart) || strings.Contains(prior, ignoreEnd) {
		begin, end := strings.Index(prior, ignoreStart), strings.Index(prior, ignoreEnd)
		if strings.Count(prior, ignoreStart) != 1 || strings.Count(prior, ignoreEnd) != 1 || end < begin {
			return "", errors.New("malformed Fugato ignore markers in .gitignore")
		}
		known := map[string]bool{"/.fugato-agent/": true}
		for _, profile := range profiles {
			known["/"+profile.config] = true
		}
		for _, line := range strings.Split(prior[begin+len(ignoreStart):end], "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !known[line] {
				return "", errors.New("edited Fugato ignore block; review .gitignore before setup")
			}
			entries[line] = true
		}
		prior = prior[:begin] + strings.TrimPrefix(prior[end+len(ignoreEnd):], "\n")
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	if prior != "" && !strings.HasSuffix(prior, "\n") {
		prior += "\n"
	}
	if prior != "" && !strings.HasSuffix(prior, "\n\n") {
		prior += "\n"
	}
	// Append last so earlier unignore rules cannot expose these generated files.
	return prior + ignoreStart + "\n" + strings.Join(names, "\n") + "\n" + ignoreEnd + "\n", nil
}
