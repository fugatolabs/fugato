package fugato

import (
	"net/http"
)

func registerEnvironmentRoutes(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		release, err := manager.admission.metadata.acquire(r.Context())
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		defer release()
		result, err := DiscoverProjects(r.Context(), r.URL.Query().Get("path"))
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	for _, operation := range []string{"run", "check"} {
		handler := func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Path string `json:"path"`
				Name string `json:"name"`
			}
			if err := readJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if operation == "run" {
				result, err := manager.RunNamed(r.Context(), body.Path, r.PathValue("id"), body.Name)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				writeJSON(w, http.StatusOK, result)
			} else {
				result, err := manager.CheckNamed(r.Context(), body.Path, r.PathValue("id"), body.Name)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				writeJSON(w, http.StatusOK, result)
			}
		}
		mux.HandleFunc("POST /v1/projects/"+operation, handler)
		mux.HandleFunc("POST /v1/runtimes/{id}/"+operation, handler)
	}
}
