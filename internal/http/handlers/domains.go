package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"bebii-seo-dashboard/internal/db"
)

var appSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,126}$`)

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
	cmdLogs, err := db.ListServerCommandLogsForUser(h.DB, me.ID, 100)
	if err != nil {
		http.Error(w, "cannot load server logs", http.StatusInternalServerError)
		return
	}
	flashMsg := popDashboardFlash(w, r)
	hostByServerID := make(map[int64]string, len(servers))
	for _, s := range servers {
		hostByServerID[s.ID] = s.Host
	}
	userLogRows := make([]UserServerLogRow, len(cmdLogs))
	for i := range cmdLogs {
		userLogRows[i] = UserServerLogRow{
			ServerCommandLog: cmdLogs[i],
			Host:             hostByServerID[cmdLogs[i].ServerID],
		}
	}
	vd := ViewData{
		Title:          "My Domains",
		Me:             me,
		Message:        flashMsg,
		Domains:        domains,
		Servers:        servers,
		UserServerLogs: userLogRows,
	}
	if headSHA, _, ghErr := getCachedBebiiBlogMainHEAD(context.WithoutCancel(r.Context()), false); ghErr == nil && headSHA != "" {
		ack, _ := db.GetUserBlogRepoAck(h.DB, me.ID)
		if !strings.EqualFold(strings.TrimSpace(headSHA), strings.TrimSpace(ack)) {
			vd.BlogMainUpdateAvailable = true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = h.Templates.ExecuteTemplate(w, "dashboard.html", vd)
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
				ServerID:   s.ID,
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
				ServerID:   s.ID,
				ServerName: s.Name,
				Path:       line,
				Status:     "ok",
				CanDelete:  appSlugPattern.MatchString(line),
			})
			added = true
		}
		if !added {
			rows = append(rows, BlogPathRow{
				ServerID:   s.ID,
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
	sudoAddSh := addShSudoCommand()
	var script string
	if sslOption == "y" {
		script = fmt.Sprintf(
			"printf '%%s\\n%s\\ny\\n%%s\\n\\n\\n' %s %s | %s",
			canonical,
			shellQuote(domain),
			shellQuote(sslEmail),
			sudoAddSh,
		)
	} else {
		script = fmt.Sprintf(
			"printf '%%s\\n%s\\nn\\n\\n\\n' %s | %s",
			canonical,
			shellQuote(domain),
			sudoAddSh,
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

func (h *Handler) handleDashboardBlogDeletePath(w http.ResponseWriter, r *http.Request) {
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
	appSlug := strings.TrimSpace(r.FormValue("app_slug"))
	if serverID <= 0 || appSlug == "" {
		http.Error(w, "server_id and app_slug required", http.StatusBadRequest)
		return
	}
	if !appSlugPattern.MatchString(appSlug) {
		http.Error(w, "invalid app_slug", http.StatusBadRequest)
		return
	}
	server, err := db.GetRemoteServerByIDForUser(h.DB, serverID, me.ID)
	if err != nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	// delete.sh: non-interactive with --apps (slug under /opt) and --yes (skip confirmation).
	inner := fmt.Sprintf("sudo -n /home/delete.sh --apps %s --yes", shellQuote(appSlug))
	command := "bash -lc " + shellQuote(inner)
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
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		_ = h.Templates.ExecuteTemplate(w, "partials_blog_delete_log.html", ViewData{ScriptLog: logData})
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// buildRemoteSudoGitCommand builds a remote shell fragment for scripts that read GIT_URL / GITHUB_*.
// Env vars are taken from ADD_GITHUB_TOKEN, ADD_GITHUB_USERNAME, ADD_GIT_URL (dashboard host).
// If sudoWithoutToken is true and the token is empty, runs "sudo -n " + scriptWithArgs (add.sh behavior).
// If sudoWithoutToken is false and the token is empty, returns scriptWithArgs only (legacy update.sh).
func buildRemoteSudoGitCommand(scriptWithArgs string, sudoWithoutToken bool) string {
	token := strings.TrimSpace(os.Getenv("ADD_GITHUB_TOKEN"))
	token = strings.ReplaceAll(strings.ReplaceAll(token, "\n", ""), "\r", "")
	if token == "" {
		if sudoWithoutToken {
			return "sudo -n " + scriptWithArgs
		}
		return scriptWithArgs
	}
	user := strings.TrimSpace(os.Getenv("ADD_GITHUB_USERNAME"))
	if user == "" {
		user = "x-access-token"
	}
	var b strings.Builder
	b.WriteString("sudo -n env GITHUB_TOKEN=")
	b.WriteString(shellQuote(token))
	b.WriteString(" GITHUB_USERNAME=")
	b.WriteString(shellQuote(user))
	if u := strings.TrimSpace(os.Getenv("ADD_GIT_URL")); u != "" {
		b.WriteString(" GIT_URL=")
		b.WriteString(shellQuote(u))
	}
	b.WriteByte(' ')
	b.WriteString(scriptWithArgs)
	return b.String()
}

// addShSudoCommand builds the remote command after the pipe for /home/add.sh.
// If ADD_GITHUB_TOKEN is set on the dashboard host, credentials are passed with
// sudo env ... so remote servers do not need /root/.github_token (add.sh reads env first).
func addShSudoCommand() string {
	return buildRemoteSudoGitCommand("/home/add.sh", true)
}

// updateShSudoCommand builds the inner command for /home/update.sh --all (admin run-update).
// When ADD_GITHUB_TOKEN is set, uses the same sudo env pattern as add-domain.
func updateShSudoCommand() string {
	return buildRemoteSudoGitCommand("/home/update.sh --all", false)
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

// handleDashboardRunUpdate runs /home/update.sh --all on every server linked to the current user (not only is_enabled), so all tied hosts get updated.
func (h *Handler) handleDashboardRunUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, err := h.currentUser(r)
	if err != nil || me == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	commandInput := strings.TrimSpace(strings.ToLower(r.FormValue("command_input")))
	if commandInput != "semua" {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}
	servers, err := db.ListRemoteServersByUser(h.DB, me.ID)
	if err != nil {
		http.Error(w, "cannot load servers", http.StatusInternalServerError)
		return
	}
	if len(servers) == 0 {
		http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Tidak ada server yang ditautkan ke akun Anda."), http.StatusSeeOther)
		return
	}
	successCount := 0
	for _, s := range servers {
		output, ok := runRemoteUpdateCommand(s)
		if ok {
			successCount++
		}
		_ = db.CreateServerCommandLog(h.DB, db.ServerCommandLog{
			ServerID:     s.ID,
			ServerName:   s.Name,
			CommandInput: commandInput,
			Success:      ok,
			Output:       output,
		})
	}
	// If all linked servers finished successfully, mark current HEAD as acknowledged
	// so the "update available" banner disappears until a newer commit appears.
	if successCount == len(servers) {
		if sha, _, ghErr := getCachedBebiiBlogMainHEAD(context.WithoutCancel(r.Context()), true); ghErr == nil && sha != "" {
			_ = db.SetUserBlogRepoAck(h.DB, me.ID, sha)
		}
	}
	msg := fmt.Sprintf("Run update selesai: %d/%d server berhasil.", successCount, len(servers))
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (h *Handler) handleDashboardClearServerLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, err := h.currentUser(r)
	if err != nil || me == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := db.ClearServerCommandLogsForUser(h.DB, me.ID); err != nil {
		http.Error(w, "clear logs failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Log perintah untuk server Anda telah dihapus."), http.StatusSeeOther)
}

func (h *Handler) handleDashboardDismissBlogRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	me, err := h.currentUser(r)
	if err != nil || me == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	sha, _, ghErr := getCachedBebiiBlogMainHEAD(context.WithoutCancel(r.Context()), true)
	if ghErr != nil || sha == "" {
		http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Tidak bisa mengambil commit terbaru dari GitHub. Periksa ADD_GITHUB_TOKEN atau GITHUB_TOKEN di .env."), http.StatusSeeOther)
		return
	}
	if err := db.SetUserBlogRepoAck(h.DB, me.ID, sha); err != nil {
		http.Error(w, "cannot save", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Notifikasi update blog disembunyikan sampai ada commit baru."), http.StatusSeeOther)
}
