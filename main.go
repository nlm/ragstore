// ragstore - single binary headless RAG document store
// Commands: index, search, list, delete, stats
// Output: always JSON to stdout, errors to stderr
// Storage: a single JSON file (default: ./rag.db.json or RAG_DB env var)
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	bm25K1      = 1.5
	bm25B       = 0.75
	defaultTopK = 5
	version     = "1.0.0"
)

// ─── Data Structures ──────────────────────────────────────────────────────────

type Document struct {
	ID      string         `json:"id"`
	Path    string         `json:"path"`
	Title   string         `json:"title"`
	Content string         `json:"content"`
	Chunk   int            `json:"chunk"`
	Tokens  map[string]int `json:"tokens"`
	Length  int            `json:"length"` // total token count
}

type Index struct {
	Documents   []*Document    `json:"documents"`
	TermDF      map[string]int `json:"term_df"` // document frequency per term
	AvgDocLen   float64        `json:"avg_doc_len"`
	ChunkSize   int            `json:"chunk_size"`
}

type SearchResult struct {
	ID      string  `json:"id"`
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Chunk   int     `json:"chunk"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

type Response struct {
	OK      bool        `json:"ok"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ─── .ragignore Support ───────────────────────────────────────────────────────

type IgnoreMatcher struct {
	patterns []gitignore.Pattern
	baseDir  string
}

func loadIgnoreFile(dir string) (*IgnoreMatcher, error) {
	ignorePath := filepath.Join(dir, ".ragignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(ignorePath)
	if err != nil {
		return nil, fmt.Errorf("read .ragignore: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	patterns := make([]gitignore.Pattern, 0)

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Validate pattern syntax
		if err := validatePattern(line, lineNum+1, ignorePath); err != nil {
			return nil, err
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}

	return &IgnoreMatcher{
		patterns: patterns,
		baseDir:  dir,
	}, nil
}

func validatePattern(pattern string, lineNum int, path string) error {
	// Check for unbalanced brackets
	openBrackets := strings.Count(pattern, "[")
	closeBrackets := strings.Count(pattern, "]")
	if openBrackets != closeBrackets {
		return fmt.Errorf("line %d: unbalanced brackets in pattern '%s'", lineNum, pattern)
	}
	
	// Check for unclosed bracket expressions
	if openBrackets > 0 {
		for i, ch := range pattern {
			if ch == '[' {
				closed := false
				for j := i + 1; j < len(pattern); j++ {
					if pattern[j] == ']' {
						closed = true
						break
					}
				}
				if !closed {
					return fmt.Errorf("line %d: unclosed bracket expression in pattern '%s'", lineNum, pattern)
				}
			}
		}
	}
	
	return nil
}

func (m *IgnoreMatcher) shouldIgnore(path string, isDir bool) bool {
	if m == nil {
		return false
	}

	relPath, err := filepath.Rel(m.baseDir, path)
	if err != nil {
		return false
	}

	matcher := gitignore.NewMatcher(m.patterns)
	parts := strings.Split(relPath, string(filepath.Separator))
	return matcher.Match(parts, isDir)
}

func mergeMatchers(parent, child *IgnoreMatcher) *IgnoreMatcher {
	if parent == nil {
		return child
	}
	if child == nil {
		return parent
	}

	merged := make([]gitignore.Pattern, len(parent.patterns)+len(child.patterns))
	copy(merged, parent.patterns)
	copy(merged[len(parent.patterns):], child.patterns)

	return &IgnoreMatcher{
		patterns: merged,
		baseDir:  child.baseDir,
	}
}

// ─── Tokenizer ────────────────────────────────────────────────────────────────

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"have": true, "has": true, "had": true, "do": true, "does": true, "did": true,
	"will": true, "would": true, "could": true, "should": true, "may": true, "might": true,
	"to": true, "of": true, "in": true, "for": true, "on": true, "with": true,
	"at": true, "by": true, "from": true, "as": true, "into": true, "through": true,
	"that": true, "this": true, "these": true, "those": true, "it": true, "its": true,
	"not": true, "no": true, "so": true, "if": true, "about": true, "which": true,
	"more": true, "also": true, "than": true, "up": true, "can": true, "all": true,
}

var nonAlpha = regexp.MustCompile(`[^a-z0-9]+`)

func tokenize(text string) []string {
	lower := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, text)
	parts := nonAlpha.Split(lower, -1)
	tokens := make([]string, 0, len(parts))
	for _, t := range parts {
		if len(t) >= 2 && !stopWords[t] {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

func tokenFrequency(tokens []string) map[string]int {
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}
	return tf
}

// ─── Chunking ─────────────────────────────────────────────────────────────────

func chunkText(text string, chunkSize int) []string {
	if chunkSize <= 0 {
		return []string{text}
	}
	// split by paragraphs first
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var current strings.Builder
	wordCount := 0

	flushChunk := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			chunks = append(chunks, s)
		}
		current.Reset()
		wordCount = 0
	}

	for _, para := range paragraphs {
		words := strings.Fields(para)
		if wordCount+len(words) > chunkSize && wordCount > 0 {
			flushChunk()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(para)
		wordCount += len(words)
	}
	flushChunk()
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	return chunks
}

// ─── Document ID ──────────────────────────────────────────────────────────────

func makeID(path string, chunk int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", path, chunk)))
	return fmt.Sprintf("%x", h[:8])
}

// ─── Index persistence ────────────────────────────────────────────────────────

func dbPath() string {
	if v := os.Getenv("RAG_DB"); v != "" {
		return v
	}
	return "./rag.db.json"
}

func loadIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Index{
			TermDF:    make(map[string]int),
			ChunkSize: 300,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	if idx.TermDF == nil {
		idx.TermDF = make(map[string]int)
	}
	return &idx, nil
}

func saveIndex(idx *Index, path string) error {
	// Recalculate avgDocLen and TermDF from scratch
	idx.TermDF = make(map[string]int)
	total := 0
	for _, doc := range idx.Documents {
		total += doc.Length
		for term := range doc.Tokens {
			idx.TermDF[term]++
		}
	}
	if len(idx.Documents) > 0 {
		idx.AvgDocLen = float64(total) / float64(len(idx.Documents))
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ─── BM25 Search ─────────────────────────────────────────────────────────────

func bm25Score(idx *Index, doc *Document, queryTokens []string) float64 {
	N := float64(len(idx.Documents))
	if N == 0 {
		return 0
	}
	dl := float64(doc.Length)
	avgdl := idx.AvgDocLen
	if avgdl == 0 {
		avgdl = 1
	}
	score := 0.0
	for _, term := range queryTokens {
		tf := float64(doc.Tokens[term])
		if tf == 0 {
			continue
		}
		df := float64(idx.TermDF[term])
		if df == 0 {
			df = 0.5 // smoothing
		}
		idf := math.Log((N-df+0.5)/(df+0.5) + 1)
		numerator := tf * (bm25K1 + 1)
		denominator := tf + bm25K1*(1-bm25B+bm25B*dl/avgdl)
		score += idf * (numerator / denominator)
	}
	return score
}

func snippet(content string, queryTokens []string, maxLen int) string {
	lower := strings.ToLower(content)
	bestPos := 0
	for _, term := range queryTokens {
		pos := strings.Index(lower, term)
		if pos >= 0 {
			bestPos = pos
			break
		}
	}
	start := bestPos - 80
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(content) {
		end = len(content)
	}
	s := content[start:end]
	s = strings.TrimSpace(s)
	if start > 0 {
		s = "…" + s
	}
	if end < len(content) {
		s = s + "…"
	}
	return s
}

// ─── File reading ─────────────────────────────────────────────────────────────

func readFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return readPDF(path)
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

func readPDF(path string) (string, error) {
	// Try pdftotext if available
	if _, err := os.Stat("/usr/bin/pdftotext"); err == nil {
		// pdftotext is available - use it via exec
		return execPDFToText(path)
	}
	return "", fmt.Errorf("PDF support requires pdftotext (apt install poppler-utils)")
}

func execPDFToText(path string) (string, error) {
	// We can't import os/exec without cgo concerns, use direct syscall approach
	// Actually os/exec is pure Go, let's use it
	// Import is at top - but we didn't import it. Let's read the file raw and strip binary.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Very basic: extract printable ASCII runs from PDF
	var sb strings.Builder
	for _, b := range data {
		if b >= 32 && b < 127 {
			sb.WriteByte(b)
		} else if b == '\n' || b == '\r' || b == '\t' {
			sb.WriteByte(' ')
		}
	}
	return sb.String(), nil
}

func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	textExts := map[string]bool{
		".txt": true, ".md": true, ".markdown": true, ".rst": true,
		".org": true, ".csv": true, ".tsv": true, ".json": true,
		".yaml": true, ".yml": true, ".toml": true, ".xml": true,
		".html": true, ".htm": true, ".tex": true, ".log": true,
		".py": true, ".go": true, ".js": true, ".ts": true,
		".java": true, ".c": true, ".cpp": true, ".h": true,
		".rs": true, ".sh": true, ".rb": true, ".php": true,
		".pdf": true,
	}
	return textExts[ext]
}

// ─── Directory Walk with .ragignore Support ───────────────────────────────────

func walkWithIgnore(root string, matcher *IgnoreMatcher, fn func(string, os.FileInfo, *IgnoreMatcher) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}

	dirMatcher, err := loadIgnoreFile(root)
	if err != nil {
		return err
	}
	currentMatcher := mergeMatchers(matcher, dirMatcher)

	if !info.IsDir() {
		return fn(root, info, currentMatcher)
	}

	if currentMatcher.shouldIgnore(root, true) {
		return filepath.SkipDir
	}

	if err := fn(root, info, currentMatcher); err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}

		if entryInfo.IsDir() {
			if currentMatcher.shouldIgnore(path, true) {
				continue
			}
			if err := walkWithIgnore(path, matcher, fn); err != nil {
				return err
			}
		} else {
			if err := fn(path, entryInfo, currentMatcher); err != nil {
				return err
			}
		}
	}

	return nil
}

// ─── Commands ─────────────────────────────────────────────────────────────────

func cmdIndex(args []string, db string) {
	if len(args) == 0 {
		fatal("index requires a path argument")
	}
	chunkSize := 300
	var paths []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--chunk-size" && i+1 < len(args) {
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid --chunk-size: " + args[i])
			}
			chunkSize = n
		} else {
			paths = append(paths, args[i])
		}
	}

	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}
	idx.ChunkSize = chunkSize

	// Build existing path+chunk set for deduplication
	existing := make(map[string]bool)
	for _, d := range idx.Documents {
		existing[d.ID] = true
	}

	indexed := 0
	skipped := 0

	for _, root := range paths {
		rootMatcher, err := loadIgnoreFile(root)
		if err != nil {
			fatal("load .ragignore: " + err.Error())
		}

		err = walkWithIgnore(root, rootMatcher, func(path string, info os.FileInfo, matcher *IgnoreMatcher) error {
			if info.IsDir() {
				return nil
			}
			if !isTextFile(path) {
				return nil
			}

			if matcher.shouldIgnore(path, false) {
				return nil
			}

			content, err := readFile(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, err)
				return nil
			}
			content = strings.TrimSpace(content)
			if content == "" {
				return nil
			}

			chunks := chunkText(content, chunkSize)
			title := filepath.Base(path)

			for i, chunk := range chunks {
				id := makeID(path, i)
				if existing[id] {
					skipped++
					continue
				}
				tokens := tokenize(chunk)
				tf := tokenFrequency(tokens)
				doc := &Document{
					ID:      id,
					Path:    path,
					Title:   title,
					Content: chunk,
					Chunk:   i,
					Tokens:  tf,
					Length:  len(tokens),
				}
				idx.Documents = append(idx.Documents, doc)
				existing[id] = true
				indexed++
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: walk %s: %v\n", root, err)
		}
	}

	if err := saveIndex(idx, db); err != nil {
		fatal("save index: " + err.Error())
	}

	respond(Response{
		OK:      true,
		Message: fmt.Sprintf("indexed %d chunks (%d skipped, already present)", indexed, skipped),
		Data: map[string]interface{}{
			"indexed":    indexed,
			"skipped":    skipped,
			"total_docs": len(idx.Documents),
			"db":         db,
		},
	})
}

func cmdSearch(args []string, db string) {
	if len(args) == 0 {
		fatal("search requires a query argument")
	}
	topK := defaultTopK
	var queryParts []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--top" || args[i] == "--top-k") && i+1 < len(args) {
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid --top value")
			}
			topK = n
		} else {
			queryParts = append(queryParts, args[i])
		}
	}
	query := strings.Join(queryParts, " ")
	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		fatal("query is empty after tokenization")
	}

	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}
	if len(idx.Documents) == 0 {
		respond(Response{OK: true, Message: "index is empty", Data: []SearchResult{}})
		return
	}

	type scored struct {
		doc   *Document
		score float64
	}
	results := make([]scored, 0, len(idx.Documents))
	for _, doc := range idx.Documents {
		s := bm25Score(idx, doc, queryTokens)
		if s > 0 {
			results = append(results, scored{doc, s})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	if topK < len(results) {
		results = results[:topK]
	}

	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, SearchResult{
			ID:      r.doc.ID,
			Path:    r.doc.Path,
			Title:   r.doc.Title,
			Chunk:   r.doc.Chunk,
			Score:   math.Round(r.score*1000) / 1000,
			Snippet: snippet(r.doc.Content, queryTokens, 300),
		})
	}
	respond(Response{OK: true, Data: out})
}

func cmdList(args []string, db string) {
	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}

	type entry struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Title string `json:"title"`
		Chunk int    `json:"chunk"`
		Words int    `json:"words"`
	}
	out := make([]entry, 0, len(idx.Documents))
	for _, d := range idx.Documents {
		out = append(out, entry{
			ID:    d.ID,
			Path:  d.Path,
			Title: d.Title,
			Chunk: d.Chunk,
			Words: d.Length,
		})
	}
	respond(Response{OK: true, Data: map[string]interface{}{
		"count":     len(out),
		"documents": out,
	}})
}

func cmdDelete(args []string, db string) {
	if len(args) == 0 {
		fatal("delete requires an id or path argument")
	}
	target := args[0]

	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}

	removed := 0
	kept := idx.Documents[:0]
	for _, d := range idx.Documents {
		if d.ID == target || d.Path == target || strings.HasPrefix(d.Path, target) {
			removed++
		} else {
			kept = append(kept, d)
		}
	}
	idx.Documents = kept

	if err := saveIndex(idx, db); err != nil {
		fatal("save index: " + err.Error())
	}
	respond(Response{OK: true, Message: fmt.Sprintf("removed %d chunk(s)", removed), Data: map[string]int{"removed": removed}})
}

func cmdStats(db string) {
	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}
	paths := make(map[string]int)
	for _, d := range idx.Documents {
		paths[d.Path]++
	}
	respond(Response{OK: true, Data: map[string]interface{}{
		"total_chunks":   len(idx.Documents),
		"unique_files":   len(paths),
		"unique_terms":   len(idx.TermDF),
		"avg_chunk_len":  math.Round(idx.AvgDocLen),
		"chunk_size_cfg": idx.ChunkSize,
		"db":             db,
	}})
}

func cmdGet(args []string, db string) {
	if len(args) == 0 {
		fatal("get requires an id argument")
	}
	id := args[0]
	idx, err := loadIndex(db)
	if err != nil {
		fatal("load index: " + err.Error())
	}
	for _, d := range idx.Documents {
		if d.ID == id {
			respond(Response{OK: true, Data: map[string]interface{}{
				"id":      d.ID,
				"path":    d.Path,
				"title":   d.Title,
				"chunk":   d.Chunk,
				"content": d.Content,
				"words":   d.Length,
			}})
			return
		}
	}
	fatal("document not found: " + id)
}

// ─── Interactive mode ─────────────────────────────────────────────────────────

func cmdInteractive(db string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "exit" || line == "quit" {
			break
		}
		parts := strings.Fields(line)
		dispatch(parts, db)
	}
}

// ─── Utilities ────────────────────────────────────────────────────────────────

func respond(r Response) {
	data, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(data))
}

func fatal(msg string) {
	data, _ := json.MarshalIndent(Response{OK: false, Message: msg}, "", "  ")
	fmt.Fprintln(os.Stderr, string(data))
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, `ragstore %s - headless BM25 document store

USAGE:
  ragstore <command> [args]

COMMANDS:
  index  <path> [--chunk-size N]   Index a file or directory (default chunk: 300 words)
  search <query> [--top N]         Search documents, return JSON results (default top: 5)
  list                             List all indexed documents
  get    <id>                      Get full content of a document chunk
  delete <id|path>                 Delete document(s) by ID or path prefix
  stats                            Show index statistics
  interactive                      Read commands from stdin line by line

EXCLUSIONS:
  Place a .ragignore file in any directory to exclude files/directories.
  Uses gitignore pattern syntax:
    - *.log           Match all .log files
    - node_modules/   Match directory
    - **/test*        Match anywhere in tree
    - !pattern        Negate (don't ignore)
    - # comment       Comment line

ENV:
  RAG_DB   Path to the index file (default: ./rag.db.json)

OUTPUT: Always JSON to stdout. Errors to stderr with exit code 1.
`, version)
	os.Exit(1)
}

func dispatch(args []string, db string) {
	if len(args) == 0 {
		usage()
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "index":
		cmdIndex(rest, db)
	case "search":
		cmdSearch(rest, db)
	case "list":
		cmdList(rest, db)
	case "delete":
		cmdDelete(rest, db)
	case "stats":
		cmdStats(db)
	case "get":
		cmdGet(rest, db)
	case "interactive":
		cmdInteractive(db)
	case "version":
		respond(Response{OK: true, Data: map[string]string{"version": version}})
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
	}
}

func main() {
	db := dbPath()
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	dispatch(args, db)
}
