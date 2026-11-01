package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LoadCommandPolicy reads a daemon-owned policy rather than branch-controlled
// project configuration. Missing files deny every command. Reloading on each
// call lets a user change the policy without restarting the daemon.
func LoadCommandPolicy(stateDir, runtimeID string) (CommandPolicy, error) {
	if err := ValidateID(runtimeID); err != nil {
		return CommandPolicy{}, err
	}
	path := filepath.Join(stateDir, "command-policy.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return CommandPolicy{}, nil
	}
	if err != nil {
		return CommandPolicy{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return CommandPolicy{}, errors.New("command-policy.json must be a user-only regular file no larger than 64 KiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CommandPolicy{}, err
	}
	var document struct {
		Version  int                              `json:"version"`
		Runtimes map[string]map[string][][]string `json:"runtimes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return CommandPolicy{}, fmt.Errorf("invalid command-policy.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return CommandPolicy{}, errors.New("command-policy.json must contain one JSON object")
	}
	if document.Version != 1 {
		return CommandPolicy{}, errors.New("command-policy.json requires version 1")
	}
	for id := range document.Runtimes {
		if err := ValidateID(id); err != nil {
			return CommandPolicy{}, fmt.Errorf("invalid policy runtime ID %q", id)
		}
	}
	return CommandPolicy{Commands: document.Runtimes[runtimeID]}, nil
}
