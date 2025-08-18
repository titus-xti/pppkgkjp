# Pemilihan GKJ - Starter (Go + PostgreSQL)

## Persyaratan
- Go 1.20+
- PostgreSQL
- Set env vars: DATABASE_URL, VOTE_START, VOTE_END, PORT(optional)
- Admin credentials: ADMIN_USER, ADMIN_PASS (for basic auth)

Contoh:
export DATABASE_URL="postgres://user:pass@localhost:5432/pemilihan?sslmode=disable"
export VOTE_START="2025-09-01T08:00:00+07:00"
export VOTE_END="2025-09-01T18:00:00+07:00"
export PORT=8080
export ADMIN_USER=admin
export ADMIN_PASS=secret

## Migrasi
psql $DATABASE_URL -f migrate.sql

## Run
go run main.go

akses:
- Voting: http://localhost:8080/Ht67h  (atau buka http://localhost:8080 dan masukkan kode)
- Admin results (basic auth): http://localhost:8080/admin
