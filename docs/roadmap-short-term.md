# githints Short-Term Roadmap
# Lightweight, Secure, Agent-First

## Decisions locked

| Question | Decision |
|----------|----------|
| `githints brief` | Ships after Outcomes. |
| Ollama | Optional. Per-agent overrides via environment variables only. No tracked `agents.json`. |
| Notes location | `.githints/notes/` only (gitignored). |
| Notes rendering | Static HTML viewer with inline JS; markdown is the source of truth. |
| DuckDB/wiki runtime | Skipped. DuckDB is an external playground only. |
| Embeddings | Skipped. |
| Team cloud snapshot | Long-term only (see section 8). |

---

## Design principles

1. **Local-first, pure-Go, cgo-free.**
2. **Markdown is the UI.** Every human-facing artifact is plain text.
3. **Fail-closed security.** Untrusted input is sanitized and scanned before it is stored or displayed.
4. **Save agent tokens and time.**
   - One `brief` call replaces many lookups.
   - Notes prevent rediscovery of context.
   - Outcomes prevent repeating failed approaches.
5. **Cloud is optional and read-first.** Shared snapshots are a future concern.

---

## 1. Outcomes

Add outcome events to the change log.

- States: `worked`, `partial`, `failed`, `reverted`, `unknown`.
- Reference target change by `id + diff_hash` (rotation-stable).
- Version the HMAC payload so existing rows still verify.
- Hook auto-detects `git revert` and appends `reverted` outcomes.
- MCP: `record_outcome`.
- CLI: `githints outcome`.

---

## 2. `githints brief`

Ranked, token-budgeted digest.

Sections:

1. Failed / partial / reverted attempts (requires Outcomes).
2. Uncommitted / pending rows.
3. Recent commits and touched files.
4. Blast radius: top dependents of recently changed files.
5. Co-changing files.

- MCP: `brief`.
- CLI: `githints brief`.
- Wired into `AGENTS.md` / `CLAUDE.md` managed blocks.

---

## 3. Notes as markdown in `.githints/notes/`

Lightweight context pages.

- Markdown files under `.githints/notes/` (gitignored).
- YAML front matter:
  ```yaml
  title: Auth Refactor Plan
  tags: [auth, refactor]
  related_files: [internal/auth/token.go]
  related_changes: [142, 143]
  ```
- Wikilinks: `[[file.go]]`, `[[note-name.md]]`.
- Indexed in `index.db`; searchable via SQLite FTS5.
- Rendered to a static HTML viewer (`.githints/notes.html`) with inline JS for
  filtering. No TypeScript framework.

MCP tools:

- `record_note`
- `get_note`
- `search_notes`
- `get_related_notes`

CLI:

- `githints note list`
- `githints note get`
- `githints note record`
- `githints note search`

---

## 4. Export pack for humans/notebooks

- `githints export`:
  - `changes.jsonl`
  - `changes.csv`
  - `CHANGES-full.md`
  - per-file markdown hints
  - `notes/` copy
  - `manifest.json` with hashes and verify status
- `githints export -duckdb` emits `githints.duckdb.sql` for optional DuckDB
  experimentation.

---

## 5. Ollama: optional, env-var configurable

Remove Ollama from the post-commit hook. Keep it as an explicit command only.

Default config:

```json
{
  "ollama": {
    "enabled": false,
    "endpoint": "http://127.0.0.1:11434",
    "model": "qwen2.5:3b-instruct",
    "timeout_ms": 3000
  }
}
```

Per-agent override via environment variables:

```bash
GITHINTS_OLLAMA_ENABLED=1
GITHINTS_OLLAMA_MODEL=qwen2.5:1.8b-instruct
GITHINTS_OLLAMA_TIMEOUT_MS=2000
```

This is more secure than a tracked `agents.json` because it never lives in the
repo and cannot be manipulated by a clone.

---

## 6. Phased plan

| Phase | Feature |
|-------|---------|
| 0 | HMAC payload versioning + outcomes schema |
| 1 | Outcomes + revert auto-detection |
| 2 | `githints brief` |
| 3 | Notes as markdown + indexing + HTML viewer |
| 4 | Export pack + DuckDB playground SQL |
| 5 | Remove Ollama from hook; keep explicit command |

---

## 7. Security & UX checklist

- [ ] All agent inputs sanitized by `internal/textsafe`.
- [ ] All agent inputs scanned by `internal/secrets`.
- [ ] File I/O through `internal/safefs`.
- [ ] Paths validated with `filepath.IsLocal`.
- [ ] Salt never exposed in exports, snapshots, or MCP responses.
- [ ] `AGENTS.md` / `CLAUDE.md` instruct agents to call `brief` and `search_notes` at session start.
- [ ] `go build` with `CGO_ENABLED=0` succeeds.
- [ ] No new runtime dependencies in `go.mod`.

---

## 8. Long-term: team cloud snapshot

Deferred because it requires:

- encrypted packaging of `.githints/`,
- cloud storage auth (S3/R2),
- a team key or CI secret for the integrity salt,
- a safe merge strategy to avoid SQLite write conflicts across agents.

When the time comes, the likely shape is a read-only CI-published snapshot that
agents download at session start, plus local appends that merge back through the
same CI pipeline.
