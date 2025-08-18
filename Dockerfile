# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o pemilihan-pendeta .

# Final stage
FROM alpine:3.18

WORKDIR /app

# Install postgresql-client for migrations
RUN apk add --no-cache postgresql-client

# Copy the binary and assets from the builder
COPY --from=builder /app/pemilihan-pendeta .
COPY --from=builder /app/static ./static
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/migrate.sql .

# Create a non-root user
RUN adduser -D -g '' appuser \
    && chown -R appuser:appuser /app

USER appuser

# Environment variables
# ENV PORT=8080
# ENV DATABASE_URL=""
# ENV VOTE_START=""
# ENV VOTE_END=""
# ENV ADMIN_USER=admin
# ENV ADMIN_PASS=""

# Expose the application port
EXPOSE 8080

# Health check
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget --no-verbose --tries=1 --spider http://localhost:$PORT/ || exit 1

# Command to run the application
ENTRYPOINT ["/app/pemilihan-pendeta"]
