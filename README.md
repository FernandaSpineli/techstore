# TechStore

E-commerce backend for electronics, gadgets and tech accessories, written in Go.

> Work in progress — the API is being built incrementally. See the commit history for progress.

## Stack

Go · PostgreSQL · Redis · Stripe · Docker

## Running locally

Requirements: Docker with Compose v2. Go 1.27+ only if you want to run outside Docker.

```bash
cp .env.example .env
make up                          # docker compose up -d --build --wait
curl localhost:8080/readyz       # {"checks":{"postgres":"ok","redis":"ok"},"status":"ok"}
```

If ports 8080, 5432 or 6379 are already in use, change `HTTP_PORT`, `POSTGRES_PORT`
or `REDIS_PORT` in `.env`.

To run the API on the host against the containerised Postgres and Redis:

```bash
docker compose up -d --wait postgres redis
make run
```

## Development

| Command          | What it does                                  |
| ---------------- | --------------------------------------------- |
| `make test`      | All tests with the race detector              |
| `make test-unit` | Tests that need no external services          |
| `make lint`      | golangci-lint (runs in Docker, no install)    |
| `make fmt`       | gofumpt + goimports                           |
| `make build`     | Static binary in `bin/`                       |
