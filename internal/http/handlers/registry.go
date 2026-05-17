package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"bebii-seo-dashboard/internal/db"
	"bebii-seo-dashboard/internal/registry"
)

const registryMaxUploadBytes = 512 << 20 // 512 MiB

func (h *Handler) handleRegistryAPI(w http.ResponseWriter, r *http.Request) {
	if h.Registry == nil {
		http.Error(w, "registry not configured", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/registry")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		h.registryAPIList(w, r)
		return
	case len(parts) == 2 && parts[1] == "manifest" && r.Method == http.MethodGet:
		h.registryAPIManifest(w, r, parts[0])
		return
	case len(parts) == 1 && parts[0] == "latest" && r.Method == http.MethodGet:
		http.Error(w, "artifact required", http.StatusBadRequest)
		return
	case len(parts) == 2 && parts[1] == "latest" && r.Method == http.MethodGet:
		h.registryAPILatest(w, r, parts[0])
		return
	case len(parts) == 3 && r.Method == http.MethodGet:
		h.registryAPIDownload(w, r, parts[0], parts[1], parts[2])
		return
	case len(parts) == 1 && r.Method == http.MethodPost:
		h.registryAPIUpload(w, r, parts[0])
		return
	}

	http.Error(w, "not found", http.StatusNotFound)
}

func (h *Handler) registryReadAllowed(r *http.Request) bool {
	if h.RegistryReadToken == "" {
		return true
	}
	return h.registryTokenOK(r, h.RegistryReadToken)
}

func (h *Handler) registryUploadAllowed(r *http.Request) bool {
	if h.RegistryUploadToken == "" {
		return false
	}
	return h.registryTokenOK(r, h.RegistryUploadToken)
}

func (h *Handler) registryTokenOK(r *http.Request, want string) bool {
	got := strings.TrimSpace(r.Header.Get("x-registry-token"))
	if got == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			got = strings.TrimSpace(auth[7:])
		}
	}
	return got != "" && got == want
}

func (h *Handler) registryAPIList(w http.ResponseWriter, r *http.Request) {
	if !h.registryReadAllowed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	artifacts, err := h.Registry.ListArtifacts()
	if err != nil {
		http.Error(w, "cannot list artifacts", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": artifacts})
}

func (h *Handler) registryAPIManifest(w http.ResponseWriter, r *http.Request, artifact string) {
	if !h.registryReadAllowed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	m, err := h.Registry.LoadManifest(artifact)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot load manifest", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

func (h *Handler) registryAPILatest(w http.ResponseWriter, r *http.Request, artifact string) {
	if !h.registryReadAllowed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	m, err := h.Registry.LoadManifest(artifact)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot load manifest", http.StatusInternalServerError)
		return
	}
	rel, err := m.LatestRelease()
	if err != nil {
		http.Error(w, "no release", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("redirect") == "1" {
		http.Redirect(w, r, registryDownloadURL(artifact, rel.Version, rel.File), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"artifact": artifact,
		"latest":   rel.Version,
		"release":  rel,
		"download_url": registryDownloadURL(artifact, rel.Version, rel.File),
	})
}

func registryDownloadURL(artifact, version, file string) string {
	return "/api/registry/" + url.PathEscape(artifact) + "/" + url.PathEscape(version) + "/" + url.PathEscape(file)
}

func (h *Handler) registryAPIDownload(w http.ResponseWriter, r *http.Request, artifact, version, file string) {
	if !h.registryReadAllowed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f, err := h.Registry.OpenFile(artifact, version, file)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot open file", http.StatusBadRequest)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(file)+`"`)
	if st != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	http.ServeContent(w, r, filepath.Base(file), st.ModTime(), f)
}

func (h *Handler) registryAPIUpload(w http.ResponseWriter, r *http.Request, artifact string) {
	if !h.registryUploadAllowed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, registryMaxUploadBytes)

	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if version == "" {
		version = strings.TrimSpace(r.Header.Get("X-Registry-Version"))
	}
	gitSHA := strings.TrimSpace(r.URL.Query().Get("git_sha"))
	if gitSHA == "" {
		gitSHA = strings.TrimSpace(r.Header.Get("X-Git-Sha"))
	}
	filename := strings.TrimSpace(r.URL.Query().Get("filename"))

	var body io.Reader = r.Body
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		if version == "" {
			version = strings.TrimSpace(r.FormValue("version"))
		}
		if gitSHA == "" {
			gitSHA = strings.TrimSpace(r.FormValue("git_sha"))
		}
		fh, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file required", http.StatusBadRequest)
			return
		}
		defer fh.Close()
		if filename == "" && hdr != nil {
			filename = hdr.Filename
		}
		body = fh
	} else if filename == "" {
		filename = strings.TrimSpace(r.Header.Get("X-Registry-Filename"))
	}

	if version == "" || filename == "" {
		http.Error(w, "version and filename required", http.StatusBadRequest)
		return
	}

	rel, err := h.Registry.PutRelease(artifact, version, filename, body, gitSHA)
	if err != nil {
		h.writeRegistryError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"release":      rel,
		"download_url": registryDownloadURL(artifact, rel.Version, rel.File),
	})
}

func (h *Handler) writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrInvalidName), errors.Is(err, registry.ErrInvalidFile):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, registry.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) handleAdminRegistry(w http.ResponseWriter, r *http.Request) {
	if h.Registry == nil {
		http.Error(w, "registry not configured (set REGISTRY_ROOT)", http.StatusServiceUnavailable)
		return
	}
	me, _ := h.currentUser(r)

	switch r.Method {
	case http.MethodGet:
		h.renderAdminRegistry(w, r, me, strings.TrimSpace(r.URL.Query().Get("msg")))
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, registryMaxUploadBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Redirect(w, r, "/admin/registry?msg="+url.QueryEscape("Upload gagal: form tidak valid."), http.StatusSeeOther)
			return
		}
		artifact := strings.ToLower(strings.TrimSpace(r.FormValue("artifact")))
		version := strings.TrimSpace(r.FormValue("version"))
		gitSHA := strings.TrimSpace(r.FormValue("git_sha"))
		fh, hdr, err := r.FormFile("file")
		if err != nil {
			http.Redirect(w, r, "/admin/registry?msg="+url.QueryEscape("Pilih file .tar.gz atau .zip."), http.StatusSeeOther)
			return
		}
		defer fh.Close()
		filename := ""
		if hdr != nil {
			filename = hdr.Filename
		}
		if _, err := h.Registry.PutRelease(artifact, version, filename, fh, gitSHA); err != nil {
			http.Redirect(w, r, "/admin/registry?artifact="+url.QueryEscape(artifact)+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/registry?artifact="+url.QueryEscape(artifact)+"&msg="+url.QueryEscape("Upload berhasil: "+artifact+" v"+version), http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminRegistryActions(w http.ResponseWriter, r *http.Request) {
	if h.Registry == nil {
		http.Error(w, "registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin/registry/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	artifact := ""
	if len(parts) > 0 {
		artifact = parts[0]
	}
	redirectBase := "/admin/registry"
	if artifact != "" {
		redirectBase += "?artifact=" + url.QueryEscape(artifact)
	}

	action := r.FormValue("action")
	version := strings.TrimSpace(r.FormValue("version"))

	switch action {
	case "set-latest":
		if err := h.Registry.SetLatest(artifact, version); err != nil {
			http.Redirect(w, r, redirectBase+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, redirectBase+"&msg="+url.QueryEscape("Latest di-set ke "+version), http.StatusSeeOther)
	case "delete":
		if err := h.Registry.DeleteRelease(artifact, version); err != nil {
			http.Redirect(w, r, redirectBase+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/registry?msg="+url.QueryEscape("Versi "+version+" dihapus."), http.StatusSeeOther)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (h *Handler) renderAdminRegistry(w http.ResponseWriter, r *http.Request, me *db.User, msg string) {
	if msg == "" {
		msg = strings.TrimSpace(r.URL.Query().Get("msg"))
	}
	artifacts, err := h.Registry.ListArtifacts()
	if err != nil {
		http.Error(w, "cannot list registry", http.StatusInternalServerError)
		return
	}

	selected := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("artifact")))
	var manifest *registry.Manifest
	if selected != "" {
		manifest, _ = h.Registry.LoadManifest(selected)
		if manifest == nil {
			manifest = &registry.Manifest{Artifact: selected}
		}
	}

	_ = h.Templates.ExecuteTemplate(w, "admin_registry.html", ViewData{
		Title:                    "Artifact Registry",
		Me:                       me,
		Message:                  msg,
		RegistryArtifacts:        artifacts,
		RegistryManifest:         manifest,
		RegistrySelectedArtifact: selected,
		RegistryHasUploadToken:   h.RegistryUploadToken != "",
	})
}
