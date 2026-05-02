package handlers

import (
	"net/http"
	"strings"

	"bebii-seo-dashboard/internal/db"
)

func (h *Handler) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	me, _ := h.currentUser(r)
	w.Header().Set("Cache-Control", "no-store")
	logs, err := db.ListVerifyLogs(h.DB, 200)
	if err != nil {
		http.Error(w, "cannot load logs", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "admin_logs.html", ViewData{
		Title:   "Admin Logs",
		Me:      me,
		Logs:    logs,
		Message: strings.TrimSpace(r.URL.Query().Get("msg")),
	})
}

func (h *Handler) handleAdminLogActions(w http.ResponseWriter, r *http.Request) {
	// POST-Redirect-GET actions for /admin/logs/*
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "admin" || parts[1] != "logs" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "clear" && r.Method == http.MethodPost {
		if err := db.ClearVerifyLogs(h.DB); err != nil {
			http.Error(w, "clear logs failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/logs?msg=Logs+cleared.", http.StatusSeeOther)
		return
	}
	http.NotFound(w, r)
}
