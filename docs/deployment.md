# Deployment & Production Runbook

Ten dokument opisuje proces wdrażania i uruchamiania produkcyjnego aplikacji **Balance** z wykorzystaniem kontenerów Docker, GitHub Actions CI/CD oraz `docker-compose`.

---

## 1. Wymagane zmienne środowiskowe (ENV)

Aplikacja Balance w trybie produkcyjnym korzysta z poniższych zmiennych konfiguracyjnych:

| Zmienna | Wymagana | Wartość domyślna | Opis |
|---------|----------|-------------------|------|
| `PORT` | Nie | `8080` | Port nasłuchiwania serwera HTTP wewnątrz kontenera. |
| `DATA_DIR` | Nie | `/app/data` | Ścieżka do katalogu z bazami SQLite (`main.db` oraz podkatalog `budgets/`). |
| `ALLOW_MOCK_PROVIDERS` | Nie | `false` | Zabezpieczenie przed używaniem mocków bankowych w produkcji (`false` dla środowiska live). |
| `GEMINI_API_KEY` | Tak (dla AI) | - | Klucz API Google Gemini wykorzystywany przez asystenta formuł Lua i strukturyzatora. |
| `GEMINI_MODEL` | Nie | `gemini-3.8-flash` | Model Gemini wykorzystywany do generowania formuł. |
| `GEMINI_TIMEOUT_SECONDS` | Nie | `90` | Limit czasu dla zapytań do API Gemini w sekundach. |
| `GOCARDLESS_SECRET_ID` | Opcjonalna | - | Identyfikator klucza API GoCardless (Open Banking). |
| `GOCARDLESS_SECRET_KEY` | Opcjonalna | - | Klucz prywatny API GoCardless. |
| `GOCARDLESS_ENVIRONMENT` | Nie | `live` | Środowisko GoCardless (`live` lub `sandbox`). |
| `GOCARDLESS_REDIRECT_URL` | Opcjonalna | - | URL przekierowania powrotnego po autoryzacji w banku. |

---

## 2. Uruchomienie za pomocą Docker Compose

W repozytorium znajduje się przykładowy plik konfiguracyjny [`docker-compose.prod.yml`](file:///home/ptr/Codes/balance/balance/docker-compose.prod.yml).

### Przykład `docker-compose.yml`:

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

### Instrukcja uruchomienia:

1. Utwórz plik `.env` na serwerze produkcyjnym z wymaganymi sekretami:
   ```bash
   cp .env.example .env
   # Uzupełnij GEMINI_API_KEY oraz ewentualne klucze GoCardless
   ```

2. Uruchom kontener w tle:
   ```bash
   docker compose -f docker-compose.prod.yml up -d
   ```

3. Sprawdź logi aplikacji:
   ```bash
   docker compose -f docker-compose.prod.yml logs -f balance
   ```

4. Trwałość danych:
   Wszystkie bazy SQLite przechowywane są w wolumenie `balance-prod-data` podpiętym pod ścieżkę `/app/data`.

---

## 3. Pipeline CI/CD w GitHub Actions

Pipeline CI/CD zdefiniowany jest w pliku `.github/workflows/ci-cd.yml` i składa się z dwóch etapów:

1. **Test & Build (CI)**:
   - Uruchamiany automatycznie przy każdym `push` oraz `pull_request` do gałęzi `main`.
   - Konfiguruje środowisko Go 1.27.x.
   - Uruchamia pełny zestaw testów jednostkowych, integracyjnych oraz BDD na mockach (`go test -v ./...` z `ALLOW_MOCK_PROVIDERS=true`).
   - Weryfikuje kompilację binarnej wersji serwera (`go build ./cmd/server`).

2. **Publish Docker Image (CD)**:
   - Uruchamiany automatycznie przy utworzeniu i wypchnięciu tagu wydaniowego (np. `v1.0.0`).
   - Loguje się do GitHub Container Registry (`ghcr.io`) za pomocą wbudowanego tokenu `GITHUB_TOKEN`.
   - Buduje zoptymalizowany wieloetapowy obraz Docker.
   - Taguje obraz jako `vX.Y.Z` oraz `latest` i publikuje w rejestrze `ghcr.io`.
