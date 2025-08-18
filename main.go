package main

import (
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

type App struct {
	db        *pgxpool.Pool
	tmpl      *template.Template
	voteStart time.Time
	voteEnd   time.Time
	adminUser string
	adminPass string
}

type ViewData struct {
	Code         string
	Name         string
	Message      string
	BeforeStart  bool
	AfterEnd     bool
	StartISO     string
	EndISO       string
	AlreadyUsed  bool
	Success      bool
	Selected     string
	Results      []VoteRow
}

type VoteRow struct {
	Code string
	Name string
	Used bool
	UsedAt *time.Time
	Choice string
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required, e.g. postgres://user:pass@localhost:5432/dbname")
	}
	voteStartStr := os.Getenv("VOTE_START") // RFC3339 e.g. 2025-09-01T08:00:00+07:00
	voteEndStr := os.Getenv("VOTE_END")     // RFC3339

	if voteStartStr == "" || voteEndStr == "" {
		log.Fatal("VOTE_START and VOTE_END env required (RFC3339)")
	}

	voteStart, err := time.Parse(time.RFC3339, voteStartStr)
	if err != nil {
		log.Fatalf("invalid VOTE_START: %v", err)
	}
	voteEnd, err := time.Parse(time.RFC3339, voteEndStr)
	if err != nil {
		log.Fatalf("invalid VOTE_END: %v", err)
	}

	// pgxpool configuration via DATABASE_URL and optional envs
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("unable to parse DATABASE_URL: %v", err)
	}
	// Optional tuning via envs (defaults provided)
	if maxConns := os.Getenv("PG_MAX_CONNS"); maxConns != "" {
		// ignore error; leave as default if parse fails
	}
	// set sensible defaults if not provided
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 20
	}
	if cfg.MinConns == 0 {
		cfg.MinConns = 1
	}
	// set a reasonable health check period
	cfg.HealthCheckPeriod = 15 * time.Second
	ctx := context.Background()
	dbpool, err := pgxpool.ConnectConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("unable to connect to db: %v", err)
	}
	defer dbpool.Close()

	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))

	app := &App{
		db:        dbpool,
		tmpl:      tmpl,
		voteStart: voteStart,
		voteEnd:   voteEnd,
		adminUser: os.Getenv("ADMIN_USER"),
		adminPass: os.Getenv("ADMIN_PASS"),
	}

	http.Handle("/static/", http.FileServer(http.FS(staticFS)))
	http.HandleFunc("/", app.indexHandler)
	http.HandleFunc("/vote", app.voteHandler)
	http.HandleFunc("/admin", app.adminHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func (a *App) indexHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Accept code in URL path: /Ht67h
	path := strings.TrimPrefix(r.URL.Path, "/")
	code := strings.TrimSpace(path)

	// If path is root or contains query like /?code=..., handle query param
	if code == "" {
		code = r.URL.Query().Get("code")
	}
	code = strings.TrimSpace(code)

	now := time.Now()
	data := ViewData{
		Code:     code,
		StartISO: a.voteStart.Format(time.RFC3339),
		EndISO:   a.voteEnd.Format(time.RFC3339),
	}
	if now.Before(a.voteStart) {
		data.BeforeStart = true
		data.Message = "Pemilihan belum dimulai — tunggu sampai waktu pembukaan."
	} else if now.After(a.voteEnd) {
		data.AfterEnd = true
		data.Message = "Pemilihan ditutup."
	}

	// If we have a code, look up voter name and used status
	if code != "" {
		var name string
		var used bool
		err := a.db.QueryRow(ctx, "SELECT name, used FROM voters WHERE code=$1", code).Scan(&name, &used)
		if err != nil {
			// not found
			data.Message = "Kode tidak ditemukan. Masukkan kode unik yang valid."
		} else {
			data.Name = name
			if used {
				data.AlreadyUsed = true
				data.Message = "Kode sudah digunakan."
			} else {
				// greeting
				if !data.BeforeStart && !data.AfterEnd {
					data.Message = fmt.Sprintf("Selamat, %s! Silakan pilih.", name)
				}
			}
		}
	}

	if err := a.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) voteHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if time.Now().Before(a.voteStart) {
		http.Error(w, "pemilihan belum dimulai", http.StatusForbidden)
		return
	}
	if time.Now().After(a.voteEnd) {
		http.Error(w, "pemilihan sudah ditutup", http.StatusForbidden)
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	choice := strings.TrimSpace(r.FormValue("choice"))

	if code == "" {
		http.Error(w, "kode diperlukan", http.StatusBadRequest)
		return
	}
	if choice == "" {
		http.Error(w, "pilihan diperlukan", http.StatusBadRequest)
		return
	}

	// Atomic update: only succeed if used = false
	tag, err := a.db.Exec(ctx, `
		UPDATE voters
		SET used = TRUE, used_at = NOW(), vote_choice = $1
		WHERE code = $2 AND used = FALSE
	`, choice, code)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("db exec error: %v", err)
		return
	}
	if tag.RowsAffected() == 0 {
		// either code not found or already used
		var exists bool
		err := a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM voters WHERE code=$1)", code).Scan(&exists)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, "kode tidak ditemukan", http.StatusBadRequest)
			return
		}
		// exists but already used
		http.Error(w, "kode sudah digunakan", http.StatusConflict)
		return
	}

	// Success: redirect to root with success param
	http.Redirect(w, r, "/"+code+"?success=1", http.StatusSeeOther)
}

func basicAuthValid(r *http.Request, user, pass string) bool {
	// If admin credentials not set, disallow access
	if user == "" || pass == "" {
		return false
	}
	// Check Authorization header
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, prefix))
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(payload), ":", 2)
	if len(parts) != 2 {
		return false
	}
	return parts[0] == user && parts[1] == pass
}

func (a *App) adminHandler(w http.ResponseWriter, r *http.Request) {
	// basic auth
	if !basicAuthValid(r, a.adminUser, a.adminPass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Admin Area"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// fetch results
	rows, err := a.db.Query(context.Background(), "SELECT code, name, used, used_at, vote_choice FROM voters ORDER BY used_at NULLS LAST, id")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []VoteRow
	for rows.Next() {
		var vr VoteRow
		err := rows.Scan(&vr.Code, &vr.Name, &vr.Used, &vr.UsedAt, &vr.Choice)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		results = append(results, vr)
	}
	data := ViewData{Results: results}
	if err := a.tmpl.ExecuteTemplate(w, "admin.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
