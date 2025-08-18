package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/joho/godotenv"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// loadTemplates loads templates from the filesystem if in development mode, otherwise uses embedded FS
func loadTemplates(useFS bool) (*template.Template, error) {
	// Create a new template with the add function
	tmpl := template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
	})

	var err error
	if useFS {
		tmpl, err = tmpl.ParseGlob("templates/*.html")
	} else {
		tmpl, err = tmpl.ParseFS(templatesFS, "templates/*.html")
	}

	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %v", err)
	}

	return tmpl, nil
}

// parseTemplates parses templates with the given functions
func parseTemplates(useFS bool) (*template.Template, error) {
	tmpl := template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
	})

	var err error
	if useFS {
		tmpl, err = tmpl.ParseGlob("templates/*.html")
	} else {
		tmpl, err = tmpl.ParseFS(templatesFS, "templates/*.html")
	}

	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %v", err)
	}

	return tmpl, nil
}

type App struct {
	db        *pgxpool.Pool
	tmpl      *template.Template
	voteStart time.Time
	voteEnd   time.Time
	adminUser string
	adminPass string
}

type AdminData struct {
	TotalVoters      int
	VotedCount       int
	NotVotedCount    int
	SetujuCount      int
	TidakSetujuCount int
	AllVoters        []VoterInfo
	VotedVoters      []VoterInfo
	NotVotedVoters   []VoterInfo
}

type VoterInfo struct {
	Code    string
	Name    string
	Used    bool
	UsedAt  sql.NullTime
	Choice  sql.NullString
}

type ViewData struct {
	Code        string
	Name        string
	Message     string
	BeforeStart bool
	AfterEnd    bool
	StartISO    string
	EndISO      string
	AlreadyUsed bool
	HasVoted    bool
	Success     bool
	Selected    string
	Results     []VoteRow
}

type VoteRow struct {
	Code   string
	Name   string
	Used   sql.NullBool
	UsedAt sql.NullTime
	Choice sql.NullString
}

func init() {
	// load .env using godotenv
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}
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

	// pgxpool configuration via DATABASE_URL
	// Check if we're in development mode (set DEV=1 in .env)
	devMode := os.Getenv("DEV") == "1"

	// Load templates
	tmpl, err := parseTemplates(devMode)
	if err != nil {
		log.Fatalf("Failed to load templates: %v", err)
	}

	if devMode {
		log.Println("Running in development mode - template auto-reload enabled")
	}

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

	tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

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

	// Get code from URL path (e.g., /Ht67h)
	path := strings.TrimPrefix(r.URL.Path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSpace(path)

	// Get code from query parameter (e.g., ?code=Ht67h)
	queryCode := r.URL.Query().Get("code")

	// Use the code from the query parameter if available, otherwise use the path
	code := queryCode
	if code == "" && path != "" && path != "index.html" {
		code = path
	}

	// Clean up the code
	code = strings.TrimSpace(code)

	// If we have a code in the path but not in the query, redirect to include it in the query
	if path != "" && path != "index.html" && code != "" && queryCode == "" {
		http.Redirect(w, r, "/?code="+url.QueryEscape(code), http.StatusFound)
		return
	}

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
			data.Message = "Kode tidak ditemukan!"
		} else {
			data.Name = name
			if used {
				data.AlreadyUsed = true
				// Check if this user has already voted
				var choice string
				err := a.db.QueryRow(ctx, "SELECT choice FROM votes WHERE code=$1", code).Scan(&choice)
				data.HasVoted = (err == nil)
				if data.HasVoted {
					data.Message = "Terima kasih telah memilih."
				} else {
					data.Message = "Kode sudah digunakan."
				}
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

	ctx := context.Background()
	
	// Get total voters count
	var totalVoters int
	err := a.db.QueryRow(ctx, "SELECT COUNT(*) FROM voters").Scan(&totalVoters)
	if err != nil {
		fmt.Println("error getting total voters:", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Get voted count and choice statistics
	var votedCount, setujuCount, tidakSetujuCount int
	err = a.db.QueryRow(ctx, `
		SELECT 
			COUNT(*) FILTER (WHERE used = true) as voted_count,
			COUNT(*) FILTER (WHERE vote_choice = 'setuju') as setuju_count,
			COUNT(*) FILTER (WHERE vote_choice = 'tidak setuju') as tidak_setuju_count
		FROM voters`).
		Scan(&votedCount, &setujuCount, &tidakSetujuCount)

	if err != nil {
		fmt.Println("error getting voting stats:", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	notVotedCount := totalVoters - votedCount

	// Get all voters with their details
	rows, err := a.db.Query(ctx, `
		SELECT code, name, used, used_at, vote_choice 
		FROM voters 
		ORDER BY used_at NULLS LAST, id`)
	if err != nil {
		fmt.Println("error getting voters:", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var allVoters, votedVoters, notVotedVoters []VoterInfo

	for rows.Next() {
		var v VoterInfo
		err := rows.Scan(&v.Code, &v.Name, &v.Used, &v.UsedAt, &v.Choice)
		if err != nil {
			fmt.Println("error scanning voter:", err)
			continue
		}

		allVoters = append(allVoters, v)
		if v.Used {
			votedVoters = append(votedVoters, v)
		} else {
			notVotedVoters = append(notVotedVoters, v)
		}
	}

	if err = rows.Err(); err != nil {
		fmt.Println("error iterating voters:", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Prepare data for template
	data := struct {
		TotalVoters      int
		VotedCount       int
		NotVotedCount    int
		SetujuCount      int
		TidakSetujuCount int
		AllVoters        []VoterInfo
		VotedVoters      []VoterInfo
		NotVotedVoters   []VoterInfo
	}{
		TotalVoters:      totalVoters,
		VotedCount:       votedCount,
		NotVotedCount:    notVotedCount,
		SetujuCount:      setujuCount,
		TidakSetujuCount: tidakSetujuCount,
		AllVoters:        allVoters,
		VotedVoters:      votedVoters,
		NotVotedVoters:   notVotedVoters,
	}

	// Execute the template
	if err := a.tmpl.ExecuteTemplate(w, "admin.html", data); err != nil {
		fmt.Println("error executing template:", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
