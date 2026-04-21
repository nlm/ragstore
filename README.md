# ragstore

Headless document search engine for LLM agents. A single Linux binary, no dependencies, no daemon — index and search in one command.

## Why ragstore

LLM agents need access to a document knowledge base without managing infrastructure. `ragstore` is designed to run in the same container as the agent: one binary to call, one index file on disk, clean JSON output ready to consume.

Search is powered by **BM25** (Okapi BM25), the ranking algorithm used by Elasticsearch and Lucene. It's the best fit for an isolated environment: no network calls, no embedding model to load, sub-millisecond latency, and excellent results on technical and domain-specific vocabulary.

---

## Installation

```bash
# Download the binary (Linux x86_64)
curl -Lo ragstore https://your-host/ragstore
chmod +x ragstore

# Or build from source (Go 1.22+)
go build -o ragstore -ldflags="-s -w" .
```

The binary is static, self-contained, 2 MB. No system dependencies required.

---

## Quick start

```bash
# Index a document directory
RAG_DB=/data/rag.db.json ./ragstore index /data/docs --chunk-size 250

# Search by topic
RAG_DB=/data/rag.db.json ./ragstore search "machine learning neural networks" --top 5
```

Output:

```json
{
  "ok": true,
  "data": [
    {
      "id": "211ccacbd4cf3c78",
      "path": "/data/docs/ml_intro.md",
      "title": "ml_intro.md",
      "chunk": 0,
      "score": 5.571,
      "snippet": "…Supervised learning uses labelled data to train classification and regression models. Unsupervised learning discovers hidden patterns in unlabelled data…"
    }
  ]
}
```

---

## Command reference

### `index <path> [--chunk-size N]`

Index a file or directory (recursive). Files already present in the index are skipped — the command is **idempotent**.

```bash
ragstore index /data/docs                    # default chunk size: 300 words
ragstore index /data/docs --chunk-size 150   # finer-grained chunks
ragstore index /data/docs /data/extra        # multiple paths
```

Supported formats: `.txt`, `.md`, `.rst`, `.org`, `.json`, `.yaml`, `.csv`, `.html`, `.py`, `.go`, `.js`, `.ts`, `.java`, `.c`, `.cpp`, `.rs`, `.sh`, and any readable text file. PDF is supported if `pdftotext` is installed (`apt install poppler-utils`).

Chunking splits text by **paragraphs** until the target word count is reached — chunks always break on natural boundaries.

| `--chunk-size` | Recommended use |
|---|---|
| 100–150 | Dense technical docs, maximum precision |
| 200–300 | General Q&A *(recommended default)* |
| 400–500 | Wide context, narrative documents |

### `search <query> [--top N]`

Search the index and return the most relevant chunks, ranked by descending BM25 score.

```bash
ragstore search "docker kubernetes orchestration" --top 5
ragstore search "refund policy terms" --top 3
```

The `snippet` field contains the most relevant excerpt from the chunk, centered around the query terms. It can be used directly to formulate an answer.

### `get <id>`

Return the full content of a chunk by its ID (obtained from `search`).

```bash
ragstore get 211ccacbd4cf3c78
```

Useful when a snippet is truncated and more context is needed.

### `list`

List all indexed documents with their path, title, chunk number, and word count.

```bash
ragstore list
```

### `delete <id|path>`

Delete one or more chunks by ID or path prefix.

```bash
ragstore delete 211ccacbd4cf3c78        # a single chunk
ragstore delete /data/docs/old.md       # all chunks from a file
ragstore delete /data/docs/archive/     # all chunks under a directory
```

### `stats`

Display index statistics.

```bash
ragstore stats
```

```json
{
  "ok": true,
  "data": {
    "total_chunks": 42,
    "unique_files": 12,
    "unique_terms": 3841,
    "avg_chunk_len": 187,
    "chunk_size_cfg": 250,
    "db": "/data/rag.db.json"
  }
}
```

### `interactive`

Read commands from stdin, one per line. Useful for sending multiple queries in batch without restarting the binary.

```bash
echo "search machine learning supervised --top 3" | ragstore interactive
```

---

## Configuration

| Environment variable | Default | Description |
|---|---|---|
| `RAG_DB` | `./rag.db.json` | Path to the index file |

The index is a self-contained JSON file. Multiple indexes can coexist by pointing `RAG_DB` to different paths.

---

## LLM agent integration

`ragstore` is designed to be referenced as an agent **skill**. The typical workflow:

1. The agent receives a question from the user
2. It extracts 3–6 representative keywords from the topic
3. It calls `ragstore search "<keywords>" --top 5`
4. It reads the `snippet` fields from the results to formulate its answer
5. If a snippet is insufficient, it calls `ragstore get <id>` for the full content

```
User:  "What is our vacation policy?"
Agent → ragstore search "vacation policy days leave" --top 3
Agent → reads snippets → formulates answer
```

The `SKILL_ragstore.md` file in this repository describes this workflow in detail and can be used directly as a system skill.

### Container integration

```dockerfile
COPY ragstore /usr/local/bin/ragstore
RUN chmod +x /usr/local/bin/ragstore
ENV RAG_DB=/data/rag.db.json

# Pre-index documents at image build time
RUN ragstore index /data/docs --chunk-size 250
```

---

## Output format

All commands return JSON to **stdout**. Errors go to **stderr** with exit code 1.

```json
// Success
{ "ok": true, "message": "...", "data": { ... } }

// Error
{ "ok": false, "message": "error description" }
```

---

## Building from source

```bash
# Prerequisites: Go 1.22+
git clone https://github.com/your-org/ragstore
cd ragstore

# Standard build
go build -o ragstore .

# Optimized build (smaller size, no debug symbols)
go build -o ragstore -ldflags="-s -w" .

# Static build (for Alpine / minimal containers)
CGO_ENABLED=0 GOOS=linux go build -o ragstore -ldflags="-s -w" .
```

The code depends only on the Go standard library. No external modules required.

---

## Algorithm

**BM25 (Okapi BM25)** with standard parameters `k1=1.5`, `b=0.75`.

Scoring of a document `d` for a query `q`:

$$\text{score}(d, q) = \sum_{t \in q} \text{IDF}(t) \cdot \frac{f(t,d) \cdot (k_1 + 1)}{f(t,d) + k_1 \left(1 - b + b \cdot \frac{|d|}{\text{avgdl}}\right)}$$

The tokenizer lowercases text, strips punctuation, and filters a multilingual stop-word list. It is language-agnostic and works equally well on English, French, or source code.

---

## Known limitations

- **No semantic search**: BM25 does not understand synonyms or paraphrasing. Compensate by broadening the query (e.g. `"car automobile vehicle"`). An LLM agent can rephrase automatically if the first results are insufficient.
- **Basic PDF support**: without `pdftotext`, PDF extraction is limited to printable ASCII characters. Install `poppler-utils` for proper extraction.
- **No incremental updates**: modifying an already-indexed file does not update its chunks. Run `delete <path>` then `index <path>` to reindex.
- **Scalability**: the index is loaded entirely into memory. Suitable for up to ~100k chunks (roughly 500 MB of raw text). Beyond that, consider Qdrant or Elasticsearch.

---

## License

MIT
