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
  model: "gpt-4o-mini"
  openai:
    api_key: "your-api-key"
    base_url: ""

search:
  nlp_expansion:
    enabled: true
    max_limit: 10
```

---

## Development & Usage

### Running Locally
To start the HTTP server with hot-reload (using Air):
```bash
make dev
```
Or run directly:
```bash
go run cmd/armi/main.go
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
- `POST /api/v1/users/register`: Register a new user.

### Document/File Endpoints
- `POST /api/v1/files`: Upload a file via multipart form. Supports tags parameter (e.g. `tags=golang,test` or multiple `tags` form entries).
- `GET /api/v1/files`: List all user files. Filter by tag with `GET /api/v1/files?tag=xxxx`.
- `GET /api/v1/files/:id`: Download raw file content.
- `GET /api/v1/files/:id/metadata`: Fetch database and physical storage status.
- `DELETE /api/v1/files/:id`: Delete a file record and clean up associated vectors (and physical storage if no other references exist).
- `GET /api/v1/files/search?q=query`: Search files semantically. Supports optional params `nlp_expansion=true`, `limit=5`, and `expansion_num=3`.

### Model Context Protocol (MCP)
- `GET /api/v1/mcp`: Establish SSE connection channel.
- `POST /api/v1/mcp/message`: Post JSON-RPC 2.0 messages (e.g., `tools/list`, `tools/call`).
