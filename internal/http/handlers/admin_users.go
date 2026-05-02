package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"bebii-seo-dashboard/internal/db"
)

func isHTMX(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true")
}

func (h *Handler) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	me, _ := h.currentUser(r)
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			if isHTMX(r) {
				http.Error(w, "bad form", http.StatusBadRequest)
			} else {
				http.Redirect(w, r, "/admin/users?msg="+url.QueryEscape("bad form"), http.StatusSeeOther)
			}
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		role := r.FormValue("role")
		if email == "" || password == "" {
			msg := "email/password required"
			if isHTMX(r) {
				http.Error(w, msg, http.StatusBadRequest)
			} else {
				http.Redirect(w, r, "/admin/users?msg="+url.QueryEscape(msg), http.StatusSeeOther)
			}
			return
		}
		if len(password) < 8 {
			msg := "password min 8 characters"
			if isHTMX(r) {
				http.Error(w, msg, http.StatusBadRequest)
			} else {
				http.Redirect(w, r, "/admin/users?msg="+url.QueryEscape(msg), http.StatusSeeOther)
			}
			return
		}
		if err := db.CreateUser(h.DB, email, password, role); err != nil {
			if isHTMX(r) {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				http.Redirect(w, r, "/admin/users?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			}
			return
		}
		if isHTMX(r) {
			h.handleAdminUsersTable(w, r)
			return
		}
		http.Redirect(w, r, "/admin/users?msg="+url.QueryEscape("User created."), http.StatusSeeOther)
		return
	}

	users, err := db.ListUsers(h.DB)
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "admin_users.html", ViewData{
		Title:   "Admin Users",
		Me:      me,
		Users:   users,
		Message: strings.TrimSpace(r.URL.Query().Get("msg")),
	})
}

func (h *Handler) handleAdminUsersTable(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(h.DB)
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "partials_user_rows.html", ViewData{Users: users})
}

func (h *Handler) handleAdminUserActions(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	userID, ok := parseUserIDFromPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case len(parts) == 4 && parts[3] == "toggle" && r.Method == http.MethodPost:
		if err := db.ToggleUserActive(h.DB, userID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.handleAdminUsersTable(w, r)
		return
	case len(parts) == 4 && parts[3] == "reset-password" && r.Method == http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		password := r.FormValue("password")
		if len(password) < 8 {
			http.Error(w, "password min 8", http.StatusBadRequest)
			return
		}
		if err := db.ResetUserPassword(h.DB, userID, password); err != nil {
			http.Error(w, "reset failed", http.StatusInternalServerError)
			return
		}
		h.handleAdminUsersTable(w, r)
		return
	case len(parts) == 4 && parts[3] == "reset-license" && r.Method == http.MethodPost:
		if _, err := db.ResetUserLicenseKey(h.DB, userID); err != nil {
			http.Error(w, "reset license failed", http.StatusInternalServerError)
			return
		}
		h.handleAdminUsersTable(w, r)
		return
	case len(parts) == 4 && parts[3] == "domains" && r.Method == http.MethodGet:
		h.renderAdminUserDomainsPage(w, r, userID)
		return
	case len(parts) == 5 && parts[3] == "domains" && parts[4] == "table" && r.Method == http.MethodGet:
		h.renderAdminUserDomainsTable(w, r, userID)
		return
	case len(parts) == 5 && parts[3] == "domains" && parts[4] == "add" && r.Method == http.MethodPost:
		h.adminAddDomain(w, r, userID)
		return
	case len(parts) == 6 && parts[3] == "domains" && r.Method == http.MethodPost && parts[5] == "delete":
		domainID, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := db.DeleteDomain(h.DB, domainID, 0, true); err != nil {
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		h.renderAdminUserDomainsTable(w, r, userID)
		return
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) renderAdminUserDomainsPage(w http.ResponseWriter, r *http.Request, userID int64) {
	me, _ := h.currentUser(r)
	target, err := h.getUserByID(userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	domains, err := db.ListDomainsByUser(h.DB, userID)
	if err != nil {
		http.Error(w, "cannot load domains", http.StatusInternalServerError)
		return
	}
	servers, err := db.ListRemoteServersByUser(h.DB, userID)
	if err != nil {
		http.Error(w, "cannot load linked servers", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "admin_domains.html", ViewData{
		Title:   "Admin Domains",
		Me:      me,
		Target:  target,
		Domains: domains,
		Servers: servers,
	})
}

func (h *Handler) renderAdminUserDomainsTable(w http.ResponseWriter, r *http.Request, userID int64) {
	domains, err := db.ListDomainsByUser(h.DB, userID)
	if err != nil {
		http.Error(w, "cannot load domains", http.StatusInternalServerError)
		return
	}
	_ = h.Templates.ExecuteTemplate(w, "partials_domain_rows_admin.html", ViewData{Domains: domains, Target: &db.User{ID: userID}})
}

func (h *Handler) adminAddDomain(w http.ResponseWriter, r *http.Request, userID int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := db.AddDomain(h.DB, userID, r.FormValue("domain")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.renderAdminUserDomainsTable(w, r, userID)
}

func (h *Handler) getUserByID(userID int64) (*db.User, error) {
	return db.GetUserByID(h.DB, userID)
}
