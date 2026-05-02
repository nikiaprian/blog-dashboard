package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"bebii-seo-dashboard/internal/db"
)

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	msgFromQuery := strings.TrimSpace(r.URL.Query().Get("msg"))
	if msgFromQuery != "" {
		setDashboardFlash(w, msgFromQuery)
		q := r.URL.Query()
		q.Del("msg")
		target := "/dashboard"
		if enc := q.Encode(); enc != "" {
			target += "?" + enc
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	me, _ := h.currentUser(r)
	domains, err := db.ListDomainsByUser(h.DB, me.ID)
	if err != nil {
		http.Error(w, "cannot load domains", http.StatusInternalServerError)
		return
	}
	servers, err := db.ListRemoteServersByUser(h.DB, me.ID)
	if err != nil {
		http.Error(w, "cannot load linked servers", http.StatusInternalServerError)
		return
	}
	flashMsg := popDashboardFlash(w, r)
	_ = h.Templates.ExecuteTemplate(w, "dashboard.html", ViewData{
		Title:   "My Domains",
		Me:      me,
		Message: flashMsg,
		Domains: domains,
		Servers: servers,
	})
}

func (h *Handler) handleDashboardDomainsTable(w http.ResponseWriter, r *http.Request) {
	me, _ := h.currentUser(r)
	domains, err := db.ListDomainsByUser(h.DB, me.ID)
	if err != nil {
		http.Error(w, "cannot load domains", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "partials_domain_rows_user.html", ViewData{Domains: domains})
}

func (h *Handler) handleDashboardAddDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, _ := h.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := db.AddDomain(h.DB, me.ID, r.FormValue("domain")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.handleDashboardDomainsTable(w, r)
}

func (h *Handler) handleDashboardDomainDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, _ := h.currentUser(r)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /dashboard/domains/{id}/delete
	if len(parts) != 4 || parts[0] != "dashboard" || parts[1] != "domains" || parts[3] != "delete" {
		http.NotFound(w, r)
		return
	}
	domainID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := db.DeleteDomain(h.DB, domainID, me.ID, false); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	h.handleDashboardDomainsTable(w, r)
}

func (h *Handler) handleDashboardBlogPathsTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, _ := h.currentUser(r)
	servers, err := db.ListRemoteServersByUser(h.DB, me.ID)
	if err != nil {
		http.Error(w, "cannot load linked servers", http.StatusInternalServerError)
		return
	}
	rows := make([]BlogPathRow, 0)
	for _, s := range servers {
		out, cmdErr := runRemoteCommand(s, "bash -lc 'ls -1 /opt 2>/dev/null'")
		if cmdErr != nil {
			rows = append(rows, BlogPathRow{
				ServerName: s.Name,
				Path:       cmdErr.Error(),
				Status:     "error",
			})
			continue
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		added := false
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			rows = append(rows, BlogPathRow{
				ServerName: s.Name,
				Path:       line,
				Status:     "ok",
			})
			added = true
		}
		if !added {
			rows = append(rows, BlogPathRow{
				ServerName: s.Name,
				Path:       "(empty /opt)",
				Status:     "empty",
			})
		}
	}
	_ = h.Templates.ExecuteTemplate(w, "partials_blog_paths_rows.html", ViewData{BlogPaths: rows})
}

func (h *Handler) handleDashboardBlogAddDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, _ := h.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	serverID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("server_id")), 10, 64)
	domain := strings.TrimSpace(r.FormValue("domain"))
	canonical := strings.TrimSpace(r.FormValue("canonical"))
	sslOption := strings.ToLower(strings.TrimSpace(r.FormValue("ssl_option")))
	sslEmail := strings.TrimSpace(r.FormValue("ssl_email"))
	if canonical != "1" && canonical != "2" {
		canonical = "1"
	}
	if sslOption != "y" && sslOption != "n" {
		sslOption = "y"
	}
	if sslOption == "y" && sslEmail == "" {
		http.Error(w, "ssl_email required when SSL is enabled", http.StatusBadRequest)
		return
	}
	if serverID <= 0 || domain == "" {
		http.Error(w, "server_id/domain required", http.StatusBadRequest)
		return
	}
	server, err := db.GetRemoteServerByIDForUser(h.DB, serverID, me.ID)
	if err != nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	// Feed answers to interactive add.sh prompts:
	// domain, canonical(1/2), SSL(y/n), [email if y], db pass(blank => auto), jwt(blank => auto).
	var script string
	if sslOption == "y" {
		script = fmt.Sprintf(
			"printf '%%s\\n%s\\ny\\n%%s\\n\\n\\n' %s %s | sudo -n /home/add.sh",
			canonical,
			shellQuote(domain),
			shellQuote(sslEmail),
		)
	} else {
		script = fmt.Sprintf(
			"printf '%%s\\n%s\\nn\\n\\n\\n' %s | sudo -n /home/add.sh",
			canonical,
			shellQuote(domain),
		)
	}
	command := "bash -lc " + shellQuote(script)
	out, cmdErr := runRemoteCommand(*server, command)
	logData := &ScriptRunLog{
		ServerName: server.Name,
		Success:    cmdErr == nil,
		Output:     strings.TrimSpace(out),
	}
	if cmdErr != nil {
		logData.Output = strings.TrimSpace(out + "\n" + cmdErr.Error())
	}
	if logData.Output == "" {
		if logData.Success {
			logData.Output = "(no output)"
		} else {
			logData.Output = "(no output, execution failed)"
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true") {
		// Hint for Nginx: allow very long upstream responses (add.sh / certbot can exceed default 60s).
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		_ = h.Templates.ExecuteTemplate(w, "partials_add_domain_log.html", ViewData{ScriptLog: logData})
		return
	}
	// Non-HTMX fallback: keep URL clean without msg query.
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func truncateLine(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func setDashboardFlash(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "dashboard_flash",
		Value:    url.QueryEscape(strings.TrimSpace(message)),
		Path:     "/dashboard",
		HttpOnly: true,
		MaxAge:   120,
		SameSite: http.SameSiteLaxMode,
	})
}

func popDashboardFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie("dashboard_flash")
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "dashboard_flash",
		Value:    "",
		Path:     "/dashboard",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
	msg, err := url.QueryUnescape(c.Value)
	if err != nil {
		return strings.TrimSpace(c.Value)
	}
	return strings.TrimSpace(msg)
}
