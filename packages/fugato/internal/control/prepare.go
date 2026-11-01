package control

// PrepareRequest copies explicitly declared local files from another worktree.
type PrepareRequest struct {
	Path   string `json:"path"`
	From   string `json:"from"`
	DryRun bool   `json:"dry_run"`
}

// PrepareReport contains paths and outcomes, never file contents.
type PrepareReport struct {
	SchemaVersion   int            `json:"schema_version"`
	ProjectPath     string         `json:"project_path"`
	SourcePath      string         `json:"source_path"`
	DryRun          bool           `json:"dry_run"`
	OK              bool           `json:"ok"`
	Error           string         `json:"error,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
	DefaultExcludes []string       `json:"default_excludes"`
	Entries         []PrepareEntry `json:"entries"`
}

type PrepareEntry struct {
	Path     string `json:"path"`
	Status   string `json:"status"`
	Required bool   `json:"required,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
