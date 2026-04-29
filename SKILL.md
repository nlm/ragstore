# SKILL: ragstore — Document Search

## Overview

`ragstore` is a headless CLI binary that indexes documents into a BM25 full-text search index and retrieves relevant chunks by topic. Use it whenever the user asks you to find information, answer questions from documents, or search a knowledge base.

**Binary location:** `/path/to/ragstore` (adapt to your deployment path)
**Index file:** configured via `RAG_DB` env var (default: `./rag.db.json`)
**Output:** always JSON to stdout. Errors go to stderr with exit code 1.

---

## Commands

### Index documents
```bash
RAG_DB=/data/rag.db.json ragstore index /path/to/docs --chunk-size 300
```
- Recursively walks directories
- Supported formats: `.txt`, `.md`, `.rst`, `.json`, `.yaml`, `.csv`, `.py`, `.go`, `.js`, `.ts`, `.html`, `.pdf` (requires `pdftotext`), and most text files
- `--chunk-size N`: target words per chunk (default: 300). Use 150-200 for precise retrieval, 400-500 for more context per result
- Already-indexed files are skipped automatically (idempotent)
- **Exclusions**: Place a `.ragignore` file in any directory to exclude files/directories (gitignore-compatible syntax)
- Returns: `{ ok, message, data: { indexed, skipped, total_docs, db } }`

### Search by topic
```bash
RAG_DB=/data/rag.db.json ragstore search "machine learning supervised" --top 5
```
- Natural language query, no special syntax needed
- `--top N`: number of results to return (default: 5)
- Returns: array of `{ id, path, title, chunk, score, snippet }`
- **snippet** contains the relevant passage — use it to answer the user's question

### Get full content of a chunk
```bash
RAG_DB=/data/rag.db.json ragstore get <id>
```
- Returns the full text of a chunk when the snippet is insufficient
- Use the `id` from a search result

### List all indexed documents
```bash
RAG_DB=/data/rag.db.json ragstore list
```
- Returns: `{ count, documents: [{ id, path, title, chunk, words }] }`

### Delete documents
```bash
RAG_DB=/data/rag.db.json ragstore delete /path/to/file.md   # by path prefix
RAG_DB=/data/rag.db.json ragstore delete <id>               # by chunk ID
```

### Index statistics
```bash
RAG_DB=/data/rag.db.json ragstore stats
```
- Returns: `{ total_chunks, unique_files, unique_terms, avg_chunk_len, chunk_size_cfg, db }`

---

## Workflow for answering user questions

1. **Always search first** before answering any question that could be in the knowledge base
2. Use 3-6 keyword terms that describe the topic — no need for full sentences
3. Read the `snippet` fields from results to formulate your answer
4. If a snippet is truncated and you need more context, call `get <id>`
5. If results are poor, try synonyms or a more specific query

### Example interaction
User: "Comment fonctionne l'apprentissage par renforcement ?"

```bash
# Step 1: search
ragstore search "apprentissage renforcement récompense agent" --top 3

# Step 2: if needed, get full content
ragstore get 211ccacbd4cf3c78
```

Then answer using the snippet/content returned.

---

## Setup: initial indexing

When setting up for a new document collection:
```bash
# Create .ragignore to exclude common directories
cat > /data/docs/.ragignore << EOF
node_modules/
build/
*.log
.git/
__pycache__/
EOF

# Index everything once
RAG_DB=/data/rag.db.json ragstore index /data/documents --chunk-size 250

# Verify
RAG_DB=/data/rag.db.json ragstore stats
```

To add new documents later, just run `index` again — already-indexed files are skipped.

---

## Tips

- **chunk-size 200-300** works best for most Q&A use cases
- **chunk-size 100-150** for dense technical docs where precision matters
- **Use `.ragignore`** to exclude `node_modules/`, `build/`, `*.log`, and other non-essential files
- The index file (`rag.db.json`) persists between sessions — no need to re-index
- Multiple indexes can coexist by using different `RAG_DB` paths
- BM25 scoring is language-agnostic — works equally well in French, English, or mixed content

---

## Error handling

All errors return JSON to stderr:
```json
{ "ok": false, "message": "error description" }
```
Exit code is 1 on error, 0 on success.
