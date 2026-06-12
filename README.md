# Armi

Armi is a high-performance document indexing, metadata extraction, and semantic search system built in Go. It supports multi-format file uploads (PDF, Word, PPT, Excel, Markdown, and TXT), automatic text extraction, OCR fallback, vector embedding generation, and hybrid (semantic + keyword) search. It also exposes a Model Context Protocol (MCP) server over SSE, allowing AI agents to query and inspect user documents dynamically.

---

## Key Features

- **Multi-Format Document Ingestion**: Support for PDF, DOC/DOCX, XLS/XLSX, PPT/PPTX, RTF, TXT, and Markdown (`.md`) files.
- **Relational Tagging**: Tag documents on upload and filter list results via `tag` query parameter (using standard many-to-many database relations).
- **Intelligent Text Extraction**: Automatically extracts plain text. Fallbacks to LLM-powered OCR (OpenAI Vision) if standard extraction yields no text for PDFs and PPTs.
- **Hybrid Semantic Search**: Uses vector embeddings (defaulting to 768 dimensions) combined with keyword search (via `gse` segmenter) to query documents.
- **NLP Query Expansion**: Uses LLM prompts in English to generate synonymous query variations for higher recall search.
- **Deduplication**: Avoids duplicate storage and redundant embedding computation by verifying global SHA3-256 file hashes before physical storage.
- **Agent Integration**: Exposes SSE endpoints conforming to the **Model Context Protocol (MCP)**, letting LLMs invoke tools like `list_files`, `search_files`, and `read_file`.

---

## Tech Stack

- **Core**: Go (Golang)
- **HTTP Framework**: Gin Gonic
- **Database (GORM)**: PostgreSQL or SQLite (dynamic choice via config)
- **Vector DB**: SQLite (using `sqlite-vec` virtual tables) or alternative providers
- **Storage**: Multi-backend support via OpenDAL (Memory, Local Filesystem, etc.)
- **AI Bindings**: Go OpenAI Client
- **Authentication**: JWT Bearer and HTTP Basic Auth

---

## Configuration

Configurations are managed via `viper`. Create a `config.yaml` or set environment variables:

```yaml
db:
  driver: "sqlite"  # sqlite or postgres
  sqlite:
    path: "armi.db"

storage:
  scheme: "fs"      # fs or memory
  root: "./uploads"

vector:
  provider: "sqlite-vec"

llm:
  model: "@default/anthropic/claude-haiku-4-5"
  query_expansion:
    enabled: true
    max_limit: 10
  ocr:
    enabled: false
  openai:
    api_key: "your-api-key"
    base_url: ""

rabbitmq:
  enabled: false
  url: "amqp://guest:guest@localhost:5672/"
  exchange: "armi.events"
  routing_key: "user.events"
  embedding_status_routing_key: "embedding.status"
  broadcast_exchange: "armi.events.broadcast"
  embedding_queue: "armi.embedding.jobs"
  embedding_status_queue: "armi.embedding.status"

auth:
  scheme: "both"     # basic, bearer, or both
  rbac:
    enabled: false   # If false (default), all group, scope, and owner checks are bypassed.
                     # This is designed for deployments delegating access control checks
                     # to an external reverse proxy / gateway like Traefik or Caddy.
```

---

## Development & Usage

### Build

Build the Armi executable before running the server or client commands:

```bash
make build
```

This produces the executable at `./build/armi`.

### Running Locally

To start the HTTP server with hot-reload (using Air):

```bash
make dev
```

Or run the compiled executable:

```bash
./build/armi serve
```

### REST Client

Use the built-in client to interact with the Armi REST API:

Client connection settings can be provided through environment variables:

```bash
export ARMI_BASE_URL=http://127.0.0.1:8080
export ARMI_USERNAME=demo
export ARMI_PASSWORD=secret
export ARMI_TIMEOUT=30s
```

Bearer authentication can use `ARMI_TOKEN` instead of `ARMI_USERNAME` and `ARMI_PASSWORD`.
Explicit command-line flags take precedence over environment variables.

```bash
# Health and users
./build/armi client health
./build/armi client user register --account demo --account-password secret
./build/armi client user me --username demo --password secret
./build/armi client user update --new-username demo2 --username demo --password secret

# Upload
./build/armi client upload file --path ./docs/report.pdf --base-url http://127.0.0.1:8080 --username demo --password secret
./build/armi client upload folder --path ./docs --base-url http://127.0.0.1:8080 --username demo --password secret

# List, download, metadata, update, delete, and search
./build/armi client file list --page 1 --page-size 20 --username demo --password secret
./build/armi client file download --id FILE_ID --output ./downloads/ --username demo --password secret
./build/armi client file metadata --id FILE_ID --username demo --password secret
./build/armi client file update --id FILE_ID --description "Updated" --tags golang,docs --username demo --password secret
./build/armi client file search --query "semantic search" --limit 5 --username demo --password secret
./build/armi client file delete --id FILE_ID --username demo --password secret
```

### Running Tests

To run unit and integration tests:

```bash
go test ./...
```

---

## API Endpoints (v1)

### Authentication

Most endpoints require basic auth (`Authorization: Basic <credentials>`) or JWT (`Authorization: Bearer <token>`).

### User Endpoints

- `POST /api/v1/users/me`: Register a new user.
- `GET /api/v1/users/me`: Get current authenticated user profile.
- `PATCH /api/v1/users/me`: Update current authenticated user profile.

### Document/File Endpoints

- `POST /api/v1/files`: Upload a file via multipart form. Supports tags parameter (e.g. `tags=golang,test` or multiple `tags` form entries).
- `GET /api/v1/files`: List all user files. Filter by tag with `GET /api/v1/files?tag=xxxx`.
- `GET /api/v1/files/:id`: Download raw file content.
- `GET /api/v1/files/:id/metadata`: Fetch database and physical storage status.
- `DELETE /api/v1/files/:id`: Delete a file record and clean up associated vectors (and physical storage if no other references exist).
- `GET /api/v1/files/search?q=query`: Search files semantically. Supports optional params `nlp_expansion=true`, `limit=5`, and `expansion_num=3`.

### Model Context Protocol (MCP)

- `GET /api/v1/mcp`: Open the Streamable HTTP listening stream for an existing session.
- `POST /api/v1/mcp`: Send JSON-RPC 2.0 messages over Streamable HTTP (e.g., `initialize`, `tools/list`, `tools/call`).
- `DELETE /api/v1/mcp`: Terminate a Streamable HTTP session.
