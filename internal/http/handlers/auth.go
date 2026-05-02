package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bebii-seo-dashboard/internal/auth"
	"bebii-seo-dashboard/internal/db"
	"gorm.io/gorm"
)

type Handler struct {
	DB                *gorm.DB
	Templates         *template.Template
	PluginSharedToken string
	GlobalKey         string
}

type ViewData struct {
	Title          string
	Me             *db.User
	Message        string
	RunUpdateNonce string
	Users          []db.User
	Domains        []db.Domain
	Target         *db.User
	Logs           []db.VerifyLog
	Servers        []db.RemoteServer
	BlogPaths      []BlogPathRow
	ScriptLog      *ScriptRunLog
	SSHKeys        []db.SSHKey
	ServerLogs     []db.ServerCommandLog
	CommandResults []CommandResult
}

type CommandResult struct {
	ServerName string
	Success    bool
	Output     string
}

type BlogPathRow struct {
	ServerID   int64
	ServerName string
	Path       string
	Status     string
	CanDelete  bool
}

type ScriptRunLog struct {
	ServerName string
	Success    bool
	Output     string
}

func New(conn *gorm.DB, tmpl *template.Template, pluginSharedToken, globalKey string) *Handler {
	return &Handler{
		DB:                conn,
		Templates:         tmpl,
		PluginSharedToken: strings.TrimSpace(pluginSharedToken),
		GlobalKey:         strings.TrimSpace(globalKey),
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/login", h.handleLogin)
	mux.HandleFunc("/logout", h.handleLogout)

	mux.HandleFunc("/admin/users", h.requireAuth("admin", h.handleAdminUsers))
	mux.HandleFunc("/admin/users/table", h.requireAuth("admin", h.handleAdminUsersTable))
	mux.HandleFunc("/admin/users/", h.requireAuth("admin", h.handleAdminUserActions))
	mux.HandleFunc("/admin/logs", h.requireAuth("admin", h.handleAdminLogs))
	mux.HandleFunc("/admin/logs/", h.requireAuth("admin", h.handleAdminLogActions))
	mux.HandleFunc("/admin/servers", h.requireAuth("admin", h.handleAdminServers))
	mux.HandleFunc("/admin/servers/", h.requireAuth("admin", h.handleAdminServerActions))

	mux.HandleFunc("/dashboard", h.requireAuth("user", h.handleDashboard))
	mux.HandleFunc("/dashboard/domains", h.requireAuth("user", h.handleDashboardAddDomain))
	mux.HandleFunc("/dashboard/domains/table", h.requireAuth("user", h.handleDashboardDomainsTable))
	mux.HandleFunc("/dashboard/domains/", h.requireAuth("user", h.handleDashboardDomainDelete))
	mux.HandleFunc("/dashboard/blogs/paths/table", h.requireAuth("user", h.handleDashboardBlogPathsTable))
	mux.HandleFunc("/dashboard/blogs/add-domain", h.requireAuth("user", h.handleDashboardBlogAddDomain))
	mux.HandleFunc("/dashboard/blogs/delete-path", h.requireAuth("user", h.handleDashboardBlogDeletePath))

	mux.HandleFunc("/api/verify", h.handleVerify)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	me, _ := h.currentUser(r)
	if me == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if me.Role == "admin" {
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		_ = h.Templates.ExecuteTemplate(w, "login.html", ViewData{Title: "Login"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		user, err := db.Authenticate(h.DB, email, password)
		if err != nil {
			_ = h.Templates.ExecuteTemplate(w, "login.html", ViewData{Title: "Login", Message: "Email/password tidak valid atau user nonaktif."})
			return
		}
		token, err := db.CreateSession(h.DB, user.ID, 24*time.Hour)
		if err != nil {
			http.Error(w, "cannot create session", http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token, 24*time.Hour)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err == nil && c.Value != "" {
		_ = db.DeleteSession(h.DB, c.Value)
	}
	auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) handleVerify(w http.ResponseWriter, r *http.Request) {
	clientIP := clientIPFromRequest(r)
	userAgent := strings.TrimSpace(r.UserAgent())

	if r.Method != http.MethodGet {
		h.writeVerifyResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", "", "")
		h.recordVerifyLog("", "", false, "Method not allowed", http.StatusMethodNotAllowed, clientIP, userAgent, "method")
		return
	}

	pluginToken := strings.TrimSpace(r.Header.Get("x-plugin-token"))
	if h.PluginSharedToken != "" && pluginToken != h.PluginSharedToken {
		h.writeVerifyResponse(w, http.StatusUnauthorized, false, "Invalid plugin token", "", "")
		h.recordVerifyLog("", "", false, "Invalid plugin token", http.StatusUnauthorized, clientIP, userAgent, "plugin-token")
		return
	}

	domain := db.NormalizeDomain(r.URL.Query().Get("domain"))
	if domain == "" {
		h.writeVerifyResponse(w, http.StatusNotFound, false, "Invalid Domain", "", "")
		h.recordVerifyLog("", "", false, "Invalid Domain", http.StatusNotFound, clientIP, userAgent, "domain")
		return
	}

	if h.isDomainBanned(domain) {
		h.writeVerifyResponse(w, http.StatusForbidden, false, "Invalid license, your domain already blocked!", domain, "")
		h.recordVerifyLog(domain, "", false, "Invalid license, your domain already blocked!", http.StatusForbidden, clientIP, userAgent, "banned")
		return
	}

	licenseKey := strings.TrimSpace(r.URL.Query().Get("license_key"))
	digital := strings.TrimSpace(r.Header.Get("x-digital"))
	if digital != "" && h.GlobalKey != "" {
		decoded, err := decodeHeader(digital, h.GlobalKey)
		if err != nil {
			h.writeVerifyResponse(w, http.StatusBadRequest, false, "Invalid License", domain, "")
			h.recordVerifyLog(domain, "", false, "Invalid License", http.StatusBadRequest, clientIP, userAgent, "decrypt")
			return
		}
		licenseKey = decoded
	}
	domain = db.NormalizeDomain(domain)
	licenseKey = strings.TrimSpace(licenseKey)
	if licenseKey == "" {
		h.writeVerifyResponse(w, http.StatusBadRequest, false, "Invalid License", domain, "")
		h.recordVerifyLog(domain, "", false, "Invalid License", http.StatusBadRequest, clientIP, userAgent, "license")
		return
	}

	allowed, err := db.VerifyLicense(h.DB, domain, licenseKey)
	if err != nil {
		h.writeVerifyResponse(w, http.StatusInternalServerError, false, "Internal Server Error", domain, licenseKey)
		h.recordVerifyLog(domain, licenseKey, false, "Internal Server Error", http.StatusInternalServerError, clientIP, userAgent, "verify")
		return
	}

	if !allowed {
		h.writeVerifyResponse(w, http.StatusNotFound, false, "Invalid Domain", domain, licenseKey)
		h.recordVerifyLog(domain, licenseKey, false, "Invalid Domain", http.StatusNotFound, clientIP, userAgent, "verify")
		return
	}

	h.writeVerifyResponse(w, http.StatusOK, true, "Website valid.", domain, licenseKey)
	h.recordVerifyLog(domain, licenseKey, true, "Website valid.", http.StatusOK, clientIP, userAgent, "verify")
}

func (h *Handler) writeVerifyResponse(w http.ResponseWriter, status int, success bool, message, domain, licenseKey string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     success,
		"message":     message,
		"status":      status,
		"domain":      domain,
		"license_key": licenseKey,
	})
}

func (h *Handler) isDomainBanned(domain string) bool {
	return false
}

func decodeHeader(header, key string) (string, error) {
	parts := strings.Split(header, ":")
	if len(parts) < 5 {
		return "", errors.New("invalid header format")
	}
	nonceBytes, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	cipherBytes, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return "", err
	}

	plainBytes := xorCipher(cipherBytes, []byte(key))
	plain := string(plainBytes)
	nonce := string(nonceBytes)
	if !strings.HasSuffix(plain, nonce) {
		return "", errors.New("tampered payload")
	}
	return strings.TrimSuffix(plain, nonce), nil
}

func xorCipher(data, key []byte) []byte {
	if len(key) == 0 {
		return data
	}
	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ key[i%len(key)]
	}
	return out
}

func (h *Handler) recordVerifyLog(domain, licenseKey string, success bool, message string, statusCode int, ipAddress, userAgent, source string) {
	_ = db.CreateVerifyLog(h.DB, db.VerifyLog{
		Domain:     domain,
		LicenseKey: licenseKey,
		Success:    success,
		Message:    message,
		StatusCode: statusCode,
		IPAddress:  ipAddress,
		UserAgent:  userAgent,
		Source:     source,
	})
}

func clientIPFromRequest(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" {
		return xri
	}
	return r.RemoteAddr
}

func (h *Handler) requireAuth(minRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, err := h.currentUser(r)
		if err != nil || me == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if minRole == "admin" && me.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (h *Handler) currentUser(r *http.Request) (*db.User, error) {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		return nil, err
	}
	return db.GetUserBySession(h.DB, c.Value)
}

func parseUserIDFromPath(path string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "admin" || parts[1] != "users" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
