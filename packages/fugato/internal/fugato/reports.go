package fugato

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

const (
	maxRetainedRuntimes = 100
	maxReportBytes      = 512 << 10
	maxReportsBytes     = 16 << 20
)

type reportEntry struct {
	reports control.LatestVerification
	bytes   int
	latest  time.Time
}

// reportCache is owned by one manager, so CLI, MCP, and editor calls share it.
type reportCache struct {
	mu      sync.Mutex
	entries map[string]*reportEntry
	bytes   int
}

func newReportCache() *reportCache { return &reportCache{entries: make(map[string]*reportEntry)} }

func (c *reportCache) latest(id string) control.LatestVerification {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[id]; entry != nil {
		return entry.reports
	}
	return control.LatestVerification{}
}

func (c *reportCache) record(id, kind string, started time.Time, result control.VerifyResult) {
	completed := time.Now().UTC()
	result.Issues = append([]control.BrowserIssue(nil), result.Issues...)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return
	}
	report := &control.VerificationReport{ReportID: hex.EncodeToString(nonce[:]), Kind: kind, StartedAt: started.UTC(), CompletedAt: completed, Result: result}
	data, err := json.Marshal(report)
	if err != nil {
		return
	}
	if len(data) > maxReportBytes {
		// Preserve the outcome and target while dropping potentially large
		// diagnostic text. The full result still reaches the original caller.
		report.Result.Logs = ""
		if len(report.Result.Issues) > 20 {
			report.Result.Issues = report.Result.Issues[:20]
		}
		for i := range report.Result.Issues {
			if len(report.Result.Issues[i].Text) > 256 {
				report.Result.Issues[i].Text = report.Result.Issues[i].Text[:256]
			}
		}
		data, err = json.Marshal(report)
		if err != nil || len(data) > maxReportBytes {
			report.Result.Issues = []control.BrowserIssue{{Kind: "truncated", Text: "report details exceeded retention limit"}}
			data, err = json.Marshal(report)
			if err != nil || len(data) > maxReportBytes {
				return
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[id]
	if entry == nil {
		entry = &reportEntry{}
		c.entries[id] = entry
	}
	c.bytes -= entry.bytes
	if kind == "http" {
		entry.reports.HTTP = report
	} else {
		entry.reports.Browser = report
	}
	entry.latest = completed
	entry.bytes = reportSize(entry.reports.HTTP) + reportSize(entry.reports.Browser)
	c.bytes += entry.bytes
	for len(c.entries) > maxRetainedRuntimes || c.bytes > maxReportsBytes {
		var oldestID string
		var oldest *reportEntry
		for candidateID, candidate := range c.entries {
			if oldest == nil || candidate.latest.Before(oldest.latest) {
				oldestID, oldest = candidateID, candidate
			}
		}
		c.bytes -= oldest.bytes
		delete(c.entries, oldestID)
	}
}

func reportSize(report *control.VerificationReport) int {
	if report == nil {
		return 0
	}
	data, _ := json.Marshal(report)
	return len(data)
}

func (c *reportCache) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[id]; entry != nil {
		c.bytes -= entry.bytes
		delete(c.entries, id)
	}
}
