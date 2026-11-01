package verify

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

//go:embed browser.cjs
var BrowserScript string

// ParseWorkerOutput is shared by backend adapters. They run BrowserScript in
// their own environment with a bounded stdout capture, then decode it here.
func ParseWorkerOutput(data []byte) (BrowserEvidence, error) {
	if len(data) > 12<<20 {
		return BrowserEvidence{}, errors.New("browser result exceeds 12 MiB")
	}
	var wire struct {
		Status     int                    `json:"status"`
		Issues     []control.BrowserIssue `json:"issues"`
		Screenshot string                 `json:"screenshot"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return BrowserEvidence{}, err
	}
	shot, err := base64.StdEncoding.DecodeString(wire.Screenshot)
	if err != nil {
		return BrowserEvidence{}, err
	}
	if len(shot) > 8<<20 {
		return BrowserEvidence{}, errors.New("browser screenshot exceeds 8 MiB")
	}
	return BrowserEvidence{Status: wire.Status, Issues: wire.Issues, Screenshot: shot}, nil
}
