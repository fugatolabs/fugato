package fugato

import (
	"errors"
	"net/http"
	"path/filepath"
)

func registerCreateRoute(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("POST /v1/runtimes/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path              string `json:"path"`
			ExpectedRuntimeID string `json:"expected_runtime_id,omitempty"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !filepath.IsAbs(body.Path) {
			writeError(w, http.StatusBadRequest, errors.New("path must be an absolute project directory path"))
			return
		}
		result, err := manager.Create(r.Context(), body.Path, body.ExpectedRuntimeID)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		// Editor lifecycle responses use the same versioned runtime DTO as
		// inspection, start, and stop, including commit and health metadata.
		item, err := (ControlService{Manager: manager}).Get(r.Context(), result.ID)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
}
