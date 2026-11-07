# leadwatch

`leadwatch` is a Go CLI and daemon that monitors public sources for people looking to hire a web developer, classifies and scores incoming posts, alerts the owner on Telegram, and drafts personalized proposal replies for human review.

> **Crucial Rule:** `leadwatch` **NEVER sends messages automatically**. All drafted replies are saved locally for manual review and owner decision.

## Features & Architecture

- **Go 1.24+ standard-library first**: (`net/http`, `log/slog`, `database/sql`, `embed`).
- **SQLite via `modernc.org/sqlite`**: Pure-Go implementation, requiring no CGO.
- **Idempotent Multi-Stage Pipeline**:
  `poll` → `normalize/dedupe` → `prefilter` → `classify` → `score` → `draft` → `notify`.
- **Public Sources**: HackerNews Jobs, RemoteOK, and WeWorkRemotely with generic user-agent (`leadwatch/0.1`) and exponential backoff on 429/5xx.
- **Owner Telegram Notifications**: Formatted alerts sent to the owner's Telegram chat.

## Directory Structure

```
cmd/leadwatch/         - Main entry point with CLI subcommands (migrate, poll, run, leads)
internal/
  config/              - Environment-based configuration, defaults, and validation
  store/               - Pure-Go SQLite storage layer and schema migrations
  source/              - Public source interfaces, HTTP client, and source adapters
  pipeline/            - Orchestration of stages communicating via the database
  classify/            - Filtering and category identification for hiring posts
  score/               - Matching score logic based on skills, terms, and budget
  draft/               - Tailored proposal drafts generator for manual review
  notify/              - Telegram owner notification dispatcher
  scheduler/           - Periodic background daemon loop with signal handling
```

## Getting Started

### 1. Configuration

Copy the example environment file:
```bash
cp .env.example .env
```

Set any optional or required environment variables:
- `DB_PATH`: SQLite database file location (default: `./leadwatch.db`).
- `LOG_LEVEL`: Logging verbosity (`DEBUG`, `INFO`, `WARN`, `ERROR`).
- `TELEGRAM_BOT_TOKEN` & `TELEGRAM_CHAT_ID`: Credentials for alerting the owner.
- `ANTHROPIC_API_KEY`: API key for optional LLM stages.

### 2. Building

```bash
go build ./cmd/leadwatch
```

### 3. Usage & Subcommands

- **Initialize / Migrate Database**:
  ```bash
  leadwatch migrate
  ```

- **Run Single Poll & Ingestion Cycle**:
  ```bash
  leadwatch poll
  ```

- **Start Background Daemon Loop**:
  ```bash
  leadwatch run
  ```

- **List Discovered Leads**:
  ```bash
  leadwatch leads -min-score 50
  ```

## Testing & Quality

All tests use `net/http/httptest` and JSON/XML fixtures without live network dependencies:

```bash
go vet ./...
go test -v ./...
```
