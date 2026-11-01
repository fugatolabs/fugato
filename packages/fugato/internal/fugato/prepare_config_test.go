package fugato

import "testing"

func TestPrepareConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		valid          bool
	}{
		{"valid", "prepare: {files: [{path: .env}, {path: local, required: false}], exclude: ['local/**/cache*'], allow: ['**/node_modules/selected']}", true},
		{"traversal", "prepare: {files: [{path: ../outside}]}", false},
		{"empty path", "prepare: {files: [{}]}", false},
		{"whole project", "prepare: {files: [{path: '.'}]}", false},
		{"absolute", "prepare: {files: [{path: /outside}]}", false},
		{"git metadata", "prepare: {files: [{path: local/.git/config}]}", false},
		{"duplicate", "prepare: {files: [{path: token}, {path: token}]}", false},
		{"invalid glob", "prepare: {exclude: ['[broken']}", false},
		{"invalid doublestar", "prepare: {allow: ['abc**def']}", false},
		{"glob traversal", "prepare: {exclude: ['../cache']}", false},
		{"glob absolute", "prepare: {exclude: ['/cache']}", false},
		{"unknown field", "prepare: {files: [{path: token, optional: true}]}", false},
		{"required null", "prepare: {files: [{path: token, required: null}]}", false},
		{"files scalar", "prepare: {files: token}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseProjectConfig([]byte("version: 1\n" + tc.fragment + "\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestPreparePattern(t *testing.T) {
	for _, tc := range []struct {
		name, pattern, path string
		want                bool
	}{
		{"recursive zero", "local/**/token", "local/token", true},
		{"trailing recursive zero", "local/**", "local", true},
		{"repeated recursive zero", "local/**/**", "local", true},
		{"recursive nested", "local/**/token", "local/a/b/token", true},
		{"recursive prefix", "**/node_modules", "a/node_modules/pkg/config", true},
		{"anchored", "cache", "a/cache", false},
		{"directory prefix", "cache/", "cache/file", true},
		{"single component", "local/*/token", "local/a/b/token", false},
		{"question", "local/token?", "local/token1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			re, err := preparePattern(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := re.MatchString(tc.path); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPrepareConfigBoundsDeclarationCount(t *testing.T) {
	if err := validatePrepareConfig(PrepareConfig{Files: make([]PrepareFile, prepareMaxEntries+1)}); err == nil {
		t.Fatal("unbounded declarations accepted")
	}
}
