package fugato

import "net/http"

func registerDestroyRoute(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("POST /v1/runtimes/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Volumes bool `json:"volumes"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := manager.Destroy(r.Context(), r.PathValue("id"), body.Volumes)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}
