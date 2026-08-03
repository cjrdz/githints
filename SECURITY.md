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
