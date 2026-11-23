# Threat model

githints is a local tool run by one person, and their agents, on their own
machine. This page is the one-screen version: what it protects, from whom, and
where each answer is explained. [SECURITY.md](../SECURITY.md) has the full
reasoning and how to report a problem; [architecture.md](architecture.md) has
the mechanisms.

## What is worth protecting

| Asset | Where it lives | Why it matters |
| --- | --- | --- |
| The change log | `.githints/store.db` (never committed) | The record of what changed and why; agents act on it |
| The salt | Per-user directory outside the repository (`githints salt path`) | Signs every row; whoever has it can re-sign the log |
| The anchors | `refs/notes/githints`, pushed with `githints anchors push` | The check on the log that does not depend on the salt |
| This machine | Files outside the repository, the terminal, the network | What a hostile repository would like to reach |
| Credentials in the tree | `.env` files, keys in code | What an agent could repeat into a record or a diff |

## Who it defends against

| Actor | Defended? | How | Details |
| --- | --- | --- | --- |
| A cloned repository (committed links, crafted `config.json`, `store.db`, file names, manifests) | **Yes** | Every file githints manages goes through `os.Root` with links refused; tracked state is refused; recorded text is neutralized before it reaches a terminal, markdown or MCP; git never runs a repository's diff drivers | [SECURITY.md: what githints defends against](../SECURITY.md#what-githints-defends-against) |
| Someone who edits or deletes rows in `store.db` later | **Detected** | The HMAC chain, and the Merkle anchors in git notes, checked by `githints verify` | [usage: integrity](usage.md#integrity-verify-anchors-and-the-salt) |
| Someone running as you, who can read the salt and re-sign the log | **Detected only through pushed anchors** | Each anchor pins a commit's rows and the whole log so far; once pushed, a rewrite no longer matches it | [SECURITY.md: threat model](../SECURITY.md#threat-model) |
| An agent that does not record why | **Nudged, not enforced** | Thin-record hints, the unrecorded-edit reminder, fallback rows from the commit hook | [usage: daily workflow](usage.md#daily-workflow) |
| One agent writing instructions for the next into a summary or note | **Mitigated, not solved** | Recorded text is labelled as data and flattened; notes are unsigned and say so | [SECURITY.md: prompt injection](../SECURITY.md#threat-model) |
| A credential an agent pastes into a record, note or diff | **Backstop only** | Known key shapes are refused or redacted; generic passwords are not recognized | [SECURITY.md: secret scanning](../SECURITY.md#threat-model) |
| The network | **Not reachable** except `githints update` | Only `internal/update` imports `net/http` (enforced by a test); it talks https to GitHub only and installs nothing that fails the release checksum | [usage: upgrading](usage.md#upgrading) |
| A compromised release | **Partly** | Checksums catch corruption; build-provenance attestations (`gh attestation verify`) show which workflow built an asset; `update` does not check attestations itself | [CONTRIBUTING: cutting a release](../CONTRIBUTING.md#cutting-a-release) |

## Out of scope

- **Other users and processes running as you.** They can read the salt and the
  store like you can; the pushed anchors are the only check they cannot quietly
  rewrite.
- **The MCP client and the model.** githints decides what goes into a tool
  result, not what the client does with it.
- **Mixed githints versions on one store.** Upgrade every copy together.

## Checks you can run

```sh
githints verify                      # chain, rendered markdown and anchors; exit 3 on a problem
githints anchors push                # make the anchors exist somewhere but this machine
githints anchors fetch && githints verify   # before a release, on a machine with the store
githints doctor                      # setup, hooks, index, and whether anchors are only local
```

In CI, which has no store or salt, check that every commit in a pull request
carries an anchor: see [Checking anchors in CI](usage.md#checking-anchors-in-ci).
