# Security Policy

## Supported versions

We support the latest released version of githints. Pre-1.0 releases may
receive security fixes as patch or minor releases.

| Version  | Supported          |
| -------- | ------------------ |
| v0.1.x   | :white_check_mark: |
| earlier  | :x:                |

## Reporting a vulnerability

If you discover a security issue, please report it privately rather than opening
a public issue.

- Open a [GitHub security advisory](https://github.com/cjrdz/githints/security/advisories/new)
  if you have access to do so.
- Otherwise, email the maintainers directly at the address associated with the
  repository owner.

Please include:

- A clear description of the issue.
- Steps to reproduce (or a proof of concept).
- The affected version(s) and platform(s).
- Any suggested remediation, if you have one.

We will respond as soon as possible and keep you informed as we work toward a
fix. Once a fix is released, we will publish a security advisory and credit the
reporter unless they prefer to remain anonymous.

## Scope

Security reports should focus on the githints tool itself (CLI, MCP server,
integrity chain, local storage, hook behavior). For third-party dependencies,
please report to the upstream project and let us know so we can bump the
vulnerable version.

## Threat model

githints is a local, single-user tool. Everything below is a known and accepted
limit, not a bug — reports about these are welcome as design discussion but will
not be treated as vulnerabilities.

**The integrity chain does not defend against the same OS user.** The HMAC salt
is stored with `0600` permissions in a per-user directory, so any process running
as you can read it and forge a chain from scratch. The compensating control is
the per-commit Merkle root written to `refs/notes/githints`: forging the database
is easy, but forging it *and* rewriting a note that may already have been pushed
is not. Treat the chain as tamper-**evident**, not tamper-proof.

**`commit_hash` is not covered by the row signature.** It is assigned after
insert, when the post-commit hook claims a pending row, so it cannot be part of
the payload the row was signed with. An attacker with database write access can
re-point a row at a different commit without breaking the chain. The Merkle note
is again what catches this. `clock_tamper_warning` *is* covered, as of the change
that added it — clearing it now breaks the signature.

**Secret scanning is a backstop, not a control.** `internal/secrets` recognizes
four high-signal shapes (AWS access key ids, GitHub tokens, PEM private keys,
JWTs). Generic `API_KEY=` assignments, passwords, and database connection strings
pass through into stored summaries and rendered markdown. Do not rely on it.

**`get_diff` redacts, it does not withhold.** Diffs are passed through the same
scrubber used before anything reaches a local model: hunks belonging to
credential-carrying paths are replaced wholesale, and lines matching a known
secret shape are replaced individually. Anything the scrubber does not recognize
reaches the agent. If a repository holds secrets that must never be shown to a
model, keep them out of the working tree.

**`.githints/config.json` is repository content.** It can therefore arrive in a
clone and enable the optional Ollama integration without you asking. The endpoint
is still forced to resolve to loopback — checked both at config load and again on
the address actually dialed — and the request path is fixed at `/api/generate`,
so this cannot reach off-box. It can choose *which* local port receives diff
content, which is enough to fingerprint local services. githints prints a warning
on stderr when a repo-supplied config enables egress.

**Prompt injection between agents is not solved.** Summaries are written by one
agent and read back by another. Recalled text is flattened to one line per field
and labelled as data rather than instructions, and the markdown renderers escape
it, but a sufficiently persuasive summary is still a summary the next model reads.

**Salt location is environment-controlled.** `GITHINTS_SALT_DIR` relocates — and
therefore chooses — the integrity key. Anyone who can set your environment has
already won; this is noted only so it is not a surprise.
