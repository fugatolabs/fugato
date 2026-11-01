package fugato

import "net/http"

func registerWorktreeRoutes(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("POST /v1/repositories/worktrees/create", func(w http.ResponseWriter, r *http.Request) {
		var input createWorktreeRequest
		if err := readJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := manager.CreateWorktree(r.Context(), input)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /v1/repositories/worktrees/remove", func(w http.ResponseWriter, r *http.Request) {
		var input removeWorktreeRequest
		if err := readJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := manager.RemoveWorktree(r.Context(), input); err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
	})
}
