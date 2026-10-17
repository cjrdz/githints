# Changelog

All notable changes to githints. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Releases before this
file are described on the [releases page](https://github.com/cjrdz/githints/releases).

Versions are tagged from `main`; see [CONTRIBUTING.md](CONTRIBUTING.md#cutting-a-release).

## Unreleased

### Upgrade notes

- **Upgrade every copy of githints that touches a repository, together.** Existing
  change logs, hints and Merkle anchors verify unchanged with the new version, and
  nothing needs migrating. The reverse does not hold once outcomes exist: a version
  that predates them reports outcome rows as `hmac does not recompute`, lists them
  as ordinary changes, and, if its post-commit hook runs, can claim them and write
  an anchor the new version then flags for good. Restart MCP servers after
  upgrading, run `githints init` so each hook names the new binary (`githints
  doctor` shows which one a hook runs), and export before going back to an older
  version.
- Run `githints init` in shared-mode repositories to update the managed
  `.gitignore` block: it now ignores `.githints/notes/` and `.githints/notes.html`.
- `notes/` is now a reserved directory for hints, like `index/`: a source file under
  a repository directory named `notes/` is still recorded, but its hint is skipped
  with a warning.
- Files with non-ASCII or unusual names are now recorded under their real names.
  Earlier versions stored git's quoted form (`"\303\251.txt"`), so agent records for
  such files were never attached to their commit. Existing rows are left as they are.
- MCP gains three tools (17 in total): `record_outcome`, `record_note` and
  `search_notes`.

### Added

- **Outcomes.** `record_outcome` and `githints outcome` record whether an earlier
  change worked, partly worked, failed or was reverted. Changes are listed with a
  `#id`, and the newest outcome shows beneath each change in history, the
  per-file hints and the brief. Outcomes are signed into the same HMAC chain and
  checked by `githints verify` (the target must exist, come earlier and have the
  same diff hash).
- **Automatic revert detection.** The post-commit hook marks changes `reverted`
  (labelled auto-detected) when a `git revert` really restored their files. The
  commit message is only a pointer: the tree must agree, so a forged message marks
  nothing.
- **Session brief.** `githints brief` and the end of `get_session_context` give a
  ranked, token-budgeted digest: failed attempts, uncommitted work, recent commits,
  blast radius from the index, co-changing files and notes.
- **Notes.** Markdown notes in `.githints/notes/` with a small front matter subset,
  `[[wikilinks]]`, FTS search, `record_note` / `search_notes`, `githints note`, and
  an offline `notes.html` viewer. Private and not integrity-checked.
- **Export pack.** `githints export -o DIR [-duckdb] [-no-notes]` writes
  `changes.jsonl`, `changes.csv`, `CHANGES-full.md`, per-file hints, notes, a
  `manifest.json` (sha256 of every file, the whole-log Merkle root and the verify
  status) and an optional DuckDB script.

### Changed

- The post-commit hook no longer calls Ollama. The `summarize` flags of `get_diff`
  and `get_recent_changes` are the only users; `source = 'llm'` rows from earlier
  versions still verify.
- `record_change` and `githints record` print the new change's id.
- `githints verify` counts changes and outcomes separately.

### Fixed

- Concurrent writers (the MCP server, the hook and the CLI) could sign against the
  same predecessor and fork the HMAC chain, and could be reported as a backwards
  clock. Signing and inserting are now one transaction that takes the write lock
  first, and `busy_timeout` now applies to every pooled connection.
- The legacy `source` column rebuild ran on every open, so a writer that landed in
  between saw a table with no `source` column. It now runs only on a store that still
  has the old constraint, inside one transaction.
- Concurrent MCP requests indexing new notes failed with `SQLITE_BUSY`.
- A commit message could forge a `reverted` outcome on a file with a non-ASCII name.

### Security

- **A link is never written through.** Every write into `.githints/` refused to
  follow a symlink or junction in its directory but not at the target, so a link
  such as `CHANGES.md -> store.db` (which a clone can ship in shared mode) destroyed
  the change log on the next record. The target and every parent are now checked.
  The same applied to hints, `graph.html` and `notes.html`.
- Notes are read as plain files only: symlinks, hard links (so a second name for the
  legacy `.salt` is not served), junctions and unsafe or secret-shaped names are
  refused, and a note is re-checked against the index before it is shown or exported.
- Exports are built from queries and never copy `.githints/`; they refuse non-empty
  or linked destinations, including a Windows junction that points into `.githints`,
  and CSV cells that would run as a spreadsheet formula are neutralized.
