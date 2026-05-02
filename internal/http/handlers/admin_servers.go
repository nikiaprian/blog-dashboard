package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"bebii-seo-dashboard/internal/auth"
	"bebii-seo-dashboard/internal/db"
	"golang.org/x/crypto/ssh"
)

const runUpdateNonceCookie = "bebii_run_update_nonce"

func (h *Handler) handleAdminServers(w http.ResponseWriter, r *http.Request) {
	me, _ := h.currentUser(r)
	switch r.Method {
	case http.MethodGet:
		nonce := issueRunUpdateNonce(w)
		h.renderAdminServersPage(w, r, me, nonce, nil, strings.TrimSpace(r.URL.Query().Get("msg")))
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		userID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("user_id")), 10, 64)
		keyID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("ssh_key_id")), 10, 64)
		port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
		if port <= 0 {
			port = 22
		}
		server := db.RemoteServer{
			Name:      strings.TrimSpace(r.FormValue("name")),
			UserID:    userID,
			Host:      strings.TrimSpace(r.FormValue("host")),
			Port:      port,
			SSHUser:   strings.TrimSpace(r.FormValue("ssh_user")),
			SSHKeyID:  keyID,
			IsEnabled: true,
		}
		if server.Name == "" || server.UserID <= 0 || server.Host == "" || server.SSHUser == "" || server.SSHKeyID <= 0 {
			http.Error(w, "name/user_id/host/ssh_user/ssh_key_id required", http.StatusBadRequest)
			return
		}
		if err := db.CreateRemoteServer(h.DB, server); err != nil {
			http.Error(w, "cannot create server", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=Server+added.", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminServerActions(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "admin" || parts[1] != "servers" {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 4 && parts[2] == "logs" && parts[3] == "clear" && r.Method == http.MethodPost {
		if err := db.ClearServerCommandLogs(h.DB); err != nil {
			http.Error(w, "clear logs failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=Command+logs+cleared.", http.StatusSeeOther)
		return
	}

	if len(parts) == 3 && parts[2] == "run-update" && r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if !validateAndConsumeRunUpdateNonce(w, r) {
			http.Error(w, "invalid run-update request", http.StatusBadRequest)
			return
		}
		// Blocks until SSH finishes on every enabled server; raise Nginx proxy_read_timeout
		// (see deploy/nginx-proxy-timeouts.conf.example) or Cloudflare will return 504 early.
		commandInput := strings.TrimSpace(strings.ToLower(r.FormValue("command_input")))
		if commandInput != "semua" {
			http.Error(w, "command_input must be 'semua'", http.StatusBadRequest)
			return
		}
		servers, err := db.ListEnabledRemoteServers(h.DB)
		if err != nil {
			http.Error(w, "cannot load servers", http.StatusInternalServerError)
			return
		}
		var results []CommandResult
		for _, s := range servers {
			output, ok := runRemoteUpdateCommand(s)
			results = append(results, CommandResult{ServerName: s.Name, Success: ok, Output: output})
			_ = db.CreateServerCommandLog(h.DB, db.ServerCommandLog{
				ServerID:     s.ID,
				ServerName:   s.Name,
				CommandInput: commandInput,
				Success:      ok,
				Output:       output,
			})
		}
		// POST-Redirect-GET to prevent refresh re-submitting POST.
		http.Redirect(w, r, "/admin/servers?msg=Update+command+executed.", http.StatusSeeOther)
		return
	}

	if len(parts) == 3 && parts[2] == "keys" && r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		keyBody := strings.TrimSpace(r.FormValue("key_body"))
		key := db.SSHKey{
			Name: strings.TrimSpace(r.FormValue("name")),
			// Keep non-empty for older schemas where key_path is NOT NULL.
			KeyPath:   "stored",
			KeyBody:   keyBody,
			IsEnabled: true,
		}
		if key.Name == "" || key.KeyBody == "" {
			http.Error(w, "name/key_body required", http.StatusBadRequest)
			return
		}
		if err := db.CreateSSHKey(h.DB, key); err != nil {
			msg := err.Error()
			if strings.Contains(strings.ToLower(msg), "duplicate") {
				http.Error(w, "key name already exists", http.StatusBadRequest)
				return
			}
			http.Error(w, "cannot create key: "+msg, http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=SSH+key+added.", http.StatusSeeOther)
		return
	}

	if len(parts) == 5 && parts[2] == "keys" && parts[4] == "delete" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := db.DeleteSSHKey(h.DB, id); err != nil {
			if errors.Is(err, db.ErrSSHKeyInUse) {
				http.Redirect(w, r, "/admin/servers?msg="+url.QueryEscape("Cannot delete: SSH key is still assigned to a server. Edit or delete that server first."), http.StatusSeeOther)
				return
			}
			http.Error(w, "delete key failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=SSH+key+deleted.", http.StatusSeeOther)
		return
	}

	if len(parts) == 4 && parts[3] == "toggle" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := db.ToggleRemoteServerEnabled(h.DB, id); err != nil {
			http.Error(w, "toggle failed", http.StatusInternalServerError)
			return
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=Server+status+updated.", http.StatusSeeOther)
		return
	}

	if len(parts) == 4 && parts[3] == "delete" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := db.DeleteRemoteServer(h.DB, id); err != nil {
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=Server+deleted.", http.StatusSeeOther)
		return
	}

	if len(parts) == 4 && parts[3] == "edit" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		userID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("user_id")), 10, 64)
		keyID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("ssh_key_id")), 10, 64)
		port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
		server := db.RemoteServer{
			ID:       id,
			Name:     strings.TrimSpace(r.FormValue("name")),
			UserID:   userID,
			Host:     strings.TrimSpace(r.FormValue("host")),
			Port:     port,
			SSHUser:  strings.TrimSpace(r.FormValue("ssh_user")),
			SSHKeyID: keyID,
		}
		if server.Name == "" || server.UserID <= 0 || server.Host == "" || server.SSHUser == "" || server.SSHKeyID <= 0 {
			http.Error(w, "name/user_id/host/ssh_user/ssh_key_id required", http.StatusBadRequest)
			return
		}
		if err := db.UpdateRemoteServer(h.DB, server); err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/servers?msg=Server+updated.", http.StatusSeeOther)
		return
	}

	http.NotFound(w, r)
}

func (h *Handler) renderAdminServersPage(w http.ResponseWriter, r *http.Request, me *db.User, nonce string, results []CommandResult, message string) {
	w.Header().Set("Cache-Control", "no-store")
	servers, err := db.ListRemoteServers(h.DB)
	if err != nil {
		http.Error(w, "cannot load servers", http.StatusInternalServerError)
		return
	}
	logs, err := db.ListServerCommandLogs(h.DB, 150)
	if err != nil {
		http.Error(w, "cannot load logs", http.StatusInternalServerError)
		return
	}
	keys, err := db.ListSSHKeys(h.DB)
	if err != nil {
		http.Error(w, "cannot load keys", http.StatusInternalServerError)
		return
	}
	users, err := db.ListUsers(h.DB)
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "admin_servers.html", ViewData{
		Title:          "Admin Servers",
		Me:             me,
		RunUpdateNonce: nonce,
		Users:          users,
		Servers:        servers,
		SSHKeys:        keys,
		ServerLogs:     logs,
		CommandResults: results,
		Message:        message,
	})
}

func issueRunUpdateNonce(w http.ResponseWriter) string {
	nonce, err := auth.NewToken(16)
	if err != nil {
		nonce = ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     runUpdateNonceCookie,
		Value:    nonce,
		Path:     "/admin/servers",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10 * 60,
	})
	return nonce
}

func validateAndConsumeRunUpdateNonce(w http.ResponseWriter, r *http.Request) bool {
	formNonce := strings.TrimSpace(r.FormValue("nonce"))
	if formNonce == "" {
		return false
	}
	c, err := r.Cookie(runUpdateNonceCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return false
	}
	if subtleConstantTimeStringCompare(formNonce, c.Value) != 1 {
		return false
	}
	// Consume nonce: prevent any automatic replays.
	http.SetCookie(w, &http.Cookie{
		Name:     runUpdateNonceCookie,
		Value:    "",
		Path:     "/admin/servers",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return true
}

func subtleConstantTimeStringCompare(a, b string) int {
	// Minimal constant-time compare to reduce timing differences.
	if len(a) != len(b) {
		return 0
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	if v == 0 {
		return 1
	}
	return 0
}

func runRemoteUpdateCommand(server db.RemoteServer) (string, bool) {
	inner := updateShSudoCommand()
	cmd := "bash -lc " + shellQuote(inner)
	out, err := runRemoteCommand(server, cmd)
	if err != nil {
		return out + "\ncommand error: " + err.Error(), false
	}
	return out, true
}

func runRemoteCommand(server db.RemoteServer, command string) (string, error) {
	var keyBytes []byte
	var err error

	if server.SSHKey != nil && strings.TrimSpace(server.SSHKey.KeyBody) != "" {
		keyBytes = []byte(strings.TrimSpace(server.SSHKey.KeyBody))
	} else {
		keyPath := strings.TrimSpace(server.SSHKeyPath)
		if keyPath == "" && server.SSHKey != nil {
			keyPath = strings.TrimSpace(server.SSHKey.KeyPath)
		}
		if keyPath == "" {
			return "", errors.New("missing ssh key source")
		}
		keyBytes, err = os.ReadFile(keyPath)
		if err != nil {
			return "", err
		}
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return "", err
	}
	client, err := ssh.Dial("tcp", server.Host+":"+strconv.Itoa(server.Port), &ssh.ClientConfig{
		User:            server.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	out, err := session.CombinedOutput(command)
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}
