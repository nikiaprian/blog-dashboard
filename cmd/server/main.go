package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"bebii-seo-dashboard/internal/db"
	apphttp "bebii-seo-dashboard/internal/http"
	"bebii-seo-dashboard/internal/http/handlers"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env from current working directory if present (does not override existing OS env).
	_ = godotenv.Load()

	cfg := db.DBConfig{
		Host:     getenv("DB_HOST", "127.0.0.1"),
		Port:     getenv("DB_PORT", "3306"),
		User:     getenv("DB_USER", "root"),
		Password: os.Getenv("DB_PASSWORD"),
		Name:     getenv("DB_NAME", "bebii_seo_dashboard"),
	}

	if err := db.EnsureDatabaseExists(cfg); err != nil {
		log.Fatalf("ensure database exists: %v", err)
	}

	conn, err := db.OpenMySQL(cfg)
	if err != nil {
		log.Fatalf("open mysql: %v", err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		log.Fatalf("open sql db: %v", err)
	}
	defer sqlDB.Close()

	if err := db.Migrate(conn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureUserLicenseKeys(conn); err != nil {
		log.Fatalf("ensure user license keys: %v", err)
	}
	if err := db.EnsureLicenseKeyUniqueIndex(conn); err != nil {
		log.Fatalf("ensure unique index on license key: %v", err)
	}
	if err := db.SeedDefaultData(conn); err != nil {
		log.Fatalf("seed default data: %v", err)
	}

	tmpl, err := template.ParseGlob("web/templates/*.html")
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	pluginSharedToken := os.Getenv("PLUGIN_SHARED_TOKEN")
	globalKey := os.Getenv("BEBII_GLOBAL_KEY")
	h := handlers.New(conn, tmpl, pluginSharedToken, globalKey)
	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	addr := listenAddr()
	server := &http.Server{
		Addr:              addr,
		Handler:           apphttp.Logging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("server listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// listenAddr: HTTP_ADDR (full, e.g. :9090 or 127.0.0.1:9090), else HTTP_PORT, else PORT, else :8080.
func listenAddr() string {
	if v := strings.TrimSpace(os.Getenv("HTTP_ADDR")); v != "" {
		return v
	}
	if p := strings.TrimSpace(os.Getenv("HTTP_PORT")); p != "" {
		return ":" + p
	}
	if p := strings.TrimSpace(os.Getenv("PORT")); p != "" {
		return ":" + p
	}
	return ":8080"
}
