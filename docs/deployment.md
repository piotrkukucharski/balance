# Deployment & Production Runbook

This document describes the production deployment and operation procedures for **Balance** using Docker containers, GitHub Actions CI/CD, and `docker-compose`.

---

## 1. Required Environment Variables (ENV)

When running in production mode, Balance uses the following environment variables:

| Variable | Required | Default Value | Description |
|---|---|---|---|
| `PORT` | No | `8080` | HTTP server listening port inside the container. |
| `DATA_DIR` | No | `/app/data` | Path to the directory housing SQLite databases (`main.db` and the `budgets/` tenant subdirectory). |
| `APP_ENV` | No | `development` | Application runtime environment (`development` vs `production`/`prod`). In production mode, developer sandboxes and mock feeds are hidden from the UI. |
| `ALLOW_REGISTRATION` | No | `true` | Public registration toggle. Set to `false` to restrict access and block new user account registrations. |
| `ALLOW_MOCK_PROVIDERS` | No | `false` | Security guard preventing mock banking adapters in production builds (`false` for live environments). |
| `GEMINI_API_KEY` | Yes (for AI) | - | Google Gemini API key used by the Lua formula assistant and structuring assistant. |
| `GEMINI_MODEL` | No | `gemini-3.8-flash` | Gemini model variant used for formula generation. |
| `GEMINI_TIMEOUT_SECONDS` | No | `90` | HTTP client timeout in seconds for Gemini API requests. |
| `GOCARDLESS_SECRET_ID` | Optional | - | GoCardless Open Banking API secret ID. |
| `GOCARDLESS_SECRET_KEY` | Optional | - | GoCardless Open Banking API secret key. |
| `GOCARDLESS_ENVIRONMENT` | No | `live` | GoCardless environment mode (`live` or `sandbox`). |
| `GOCARDLESS_REDIRECT_URL` | Optional | - | Callback redirect URL following bank authentication. |

---

## 2. Running with Docker Compose

A production-ready compose template is provided in [`docker-compose.prod.yml`](file:///home/ptr/Codes/balance/balance/docker-compose.prod.yml).

### Example `docker-compose.yml`:

```yaml
version: "3.8"

services:
  balance:
    image: ghcr.io/<owner>/balance:latest
    container_name: balance-server
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - PORT=8080
      - DATA_DIR=/app/data
      - ALLOW_REGISTRATION=${ALLOW_REGISTRATION:-true}
      - ALLOW_MOCK_PROVIDERS=false
      - GEMINI_API_KEY=${GEMINI_API_KEY}
      - GEMINI_MODEL=${GEMINI_MODEL:-gemini-3.8-flash}
      - GEMINI_TIMEOUT_SECONDS=${GEMINI_TIMEOUT_SECONDS:-90}
      - GOCARDLESS_SECRET_ID=${GOCARDLESS_SECRET_ID:-}
      - GOCARDLESS_SECRET_KEY=${GOCARDLESS_SECRET_KEY:-}
      - GOCARDLESS_ENVIRONMENT=${GOCARDLESS_ENVIRONMENT:-live}
      - GOCARDLESS_REDIRECT_URL=${GOCARDLESS_REDIRECT_URL:-https://balance.yourdomain.com/budgets/integrations/gocardless/callback}
    volumes:
      - balance-data:/app/data

volumes:
  balance-data:
    name: balance-prod-data
```

### Deployment Instructions:

1. Create a `.env` file on the production server with the required secrets:
   ```bash
   cp .env.example .env
   # Populate GEMINI_API_KEY and GoCardless credentials
   ```

2. Start the container in detached mode:
   ```bash
   docker compose -f docker-compose.prod.yml up -d
   ```

3. Check application logs:
   ```bash
   docker compose -f docker-compose.prod.yml logs -f balance
   ```

4. Data Persistence:
   All SQLite database files are stored inside the named Docker volume `balance-prod-data` mounted at `/app/data`.

---

## 3. GitHub Actions CI/CD Pipeline

The pipeline is defined in [`.github/workflows/ci-cd.yml`](file:///home/ptr/Codes/balance/balance/.github/workflows/ci-cd.yml) and consists of two primary stages:

1. **Test & Build (CI)**:
   - Triggers automatically on every `push` and `pull_request` targeting the `main` branch.
   - Sets up the Go 1.27.x environment with caching.
   - Runs the full unit, integration, and BDD test suite with mock adapters enabled (`go test -v -race ./...` with `ALLOW_MOCK_PROVIDERS=true`).
   - Verifies server binary compilation (`go build ./cmd/server`).

2. **Publish Docker Image (CD)**:
   - Triggers automatically when a release tag is pushed (e.g. `v1.0.0`).
   - Authenticates against GitHub Container Registry (`ghcr.io`) using `GITHUB_TOKEN`.
   - Builds an optimized multi-stage Docker image with CGO and SQLite support.
   - Tags the image with semantic versioning tags (`vX.Y.Z`, `vX.Y`, `latest`) and publishes it to `ghcr.io`.
