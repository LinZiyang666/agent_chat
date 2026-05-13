# M1 — Phase 1 Report (Implementation)

> Phase 1 + Phase 2 were executed as a single continuous block per
> `05-engineering-workflow.md`. This document records *what was built*; the
> companion `M1-phase2.md` records *how it was tested*.

**Date:** 2026-05-13
**Milestone scope:** `04-roadmap.md` §2 — repository skeleton.

## 1. Goal recap

Stand up the project so that `make build` produces two binaries
(`agentchat`, `agentchatd`), both respond to `--help` and `--version`, and
the directory layout from `03-architecture.md` §8 is in place ready for
M2.

## 2. Files added

### Top-level

| File | Purpose |
|------|---------|
| `go.mod` | module `github.com/LinZiyang666/agentchat`, `go 1.22` |
| `Makefile` | targets: `build`, `build-cli`, `build-daemon`, `test`, `test-race`, `cover`, `fmt`, `vet`, `tidy`, `smoke`, `clean` |
| `README.md` | project overview, build/test instructions, doc pointers |
| `LICENSE` | MIT, copyright 2026 LinZiyang666 |
| `.gitignore` | binaries, coverage artifacts, runtime data (`*.db`, `master.key`, `agentchatd.sock`, `attachments/`), editor/OS noise |

### Binaries (`cmd/`)

| File | Purpose |
|------|---------|
| `cmd/agentchat/main.go` | minimal `main()`; delegates to `cmds.Execute()` |
| `cmd/agentchat/cmds/root.go` | cobra root for the CLI; exports `Version` (build-time overridable via `-ldflags -X`) |
| `cmd/agentchat/cmds/version.go` | `version` subcommand |
| `cmd/agentchatd/main.go` | analogous, for the daemon |
| `cmd/agentchatd/cmds/root.go` | analogous |
| `cmd/agentchatd/cmds/version.go` | analogous |

### Internal packages (placeholders)

Each of the following received a `doc.go` describing its responsibility
in this project; no implementation code yet, by design (M1 is skeleton):

```
internal/api/         (HTTP routing & handlers)
internal/bot/         (platform-agnostic Provider interface)
internal/account/     (account CRUD & lifecycle)
internal/room/        (rooms & three-state account-room relation)
internal/message/     (send/recv, per-account state, replies, mentions)
internal/attachment/  (download cache & on-disk index)
internal/announcement/(room/@all/system announcements)
internal/state/       (state-view aggregation engine)
internal/auth/        (API token issuance & verification)
internal/store/       (repository interfaces)
internal/crypto/      (AES-GCM + bcrypt)
internal/config/      (TOML + env loader)
internal/errcode/     (structured error codes; exit-code mapping)
internal/health/      (system-health dimension of state view)
internal/audit/       (admin action audit log)
```

### Public package

| File | Purpose |
|------|---------|
| `pkg/client/doc.go` | placeholder for the public HTTP client SDK (consumed by the CLI and external agent SDKs) |

### End-to-end harness

| File | Purpose |
|------|---------|
| `e2e/m1-smoke.sh` | builds both binaries, exercises `--help`, `--version`, and the `version` subcommand, fails loudly on any mismatch |

## 3. Dependencies added

Per the engineering-workflow §1.8 discipline, library docs were checked
before adding:

| Package | Version | Why |
|---------|---------|-----|
| `github.com/spf13/cobra` | v1.10.2 | CLI framework, used by both binaries |
| `github.com/spf13/pflag` | v1.0.9 (indirect via cobra) | — |
| `github.com/inconshreveable/mousetrap` | v1.1.0 (indirect via cobra, Windows-only) | — |

Reference docs consulted: GitHub README + user-guide on `spf13/cobra`.
The canonical "main calls cmds.Execute()" pattern was followed.

## 4. Decisions made during implementation (not in design docs)

1. **`cobra-cli` generator not used.** The roadmap warned against
   scaffolding bloat (§2 "key technical points"). All cobra command
   wiring was written by hand. Result: ~50 LOC across both binaries,
   no boilerplate.
2. **Command subpackage named `cmds/`** (not `cmd/`) under each binary,
   to avoid a name collision with the parent `cmd/` directory in
   `cmd/agentchat/cmd/...`. This matches the layout sketched in
   `03-architecture.md` §8.
3. **`SilenceUsage: true`** on both root commands. With `SilenceUsage`
   off, any command failure dumps full usage text to stderr, which is
   hostile to agents parsing exit codes. We surface only the error.
4. **`SilenceErrors: false`** (cobra's default). Cobra prints the error
   message, `main()` adds a blank line and exits non-zero. Net effect:
   one error line + clean exit code, agent- and human-friendly.
5. **`Version` is a package-level `var`, not const.** This is required
   to make it `-ldflags -X`-overridable at build time (`-X` cannot set
   constants).
6. **Each placeholder package contains a `doc.go` only.** No empty `.go`
   stub with a `// TODO`. `go build ./...` is happy because a package
   with only a `doc.go` is a valid (empty) package.
7. **Coverage-target relaxation for M1.** The exit criterion in §05 is
   "≥ 70% on new biz code, or explained." There is **no biz code** in
   M1; the only testable code is the two `cmds` packages. Both are at
   75.0%, which exceeds the bar locally. The project-total figure of
   33.3% is dominated by the 16 placeholder packages with zero
   executable statements and is not a meaningful indicator.

## 5. Deferred items (intentional)

These appear in `03-architecture.md` but are not part of M1:

- Persistent flags on the root commands (e.g. `--config`, `--token`):
  defer to M2 when first command needs them.
- Build-time injection of git commit / build date into Version: not
  needed until we cut releases; revisit at M8.
- `staticcheck` / `golangci-lint` integration: leave for M8 (CI prep).
- `shell completion` subcommand: M8.

## 6. Demo

Reproduce the milestone goal in three commands:

```bash
make build
make smoke
./bin/agentchat version
```

Expected output of the last command:

```
agentchat dev
```

The full smoke script also exercises `--help`, `--version` (cobra's
built-in flag), and the daemon's matching outputs.

## 7. Layout snapshot

```
agent_chat/
├── .gitignore
├── LICENSE
├── Makefile
├── README.md
├── cmd/
│   ├── agentchat/
│   │   ├── cmds/{root,version,version_test}.go
│   │   └── main.go
│   └── agentchatd/
│       ├── cmds/{root,version,version_test}.go
│       └── main.go
├── docs/
│   ├── 00-overview.md
│   ├── 01-requirements.md
│   ├── 02-requirements-final.md
│   ├── 03-architecture.md
│   ├── 04-roadmap.md
│   ├── 05-engineering-workflow.md
│   └── milestones/
│       ├── M1-phase1.md  ← this file
│       └── M1-phase2.md
├── e2e/
│   └── m1-smoke.sh
├── go.mod
├── go.sum
├── internal/
│   ├── account/     doc.go
│   ├── announcement/doc.go
│   ├── api/         doc.go
│   ├── attachment/  doc.go
│   ├── audit/       doc.go
│   ├── auth/        doc.go
│   ├── bot/         doc.go
│   ├── config/      doc.go
│   ├── crypto/      doc.go
│   ├── errcode/     doc.go
│   ├── health/      doc.go
│   ├── message/     doc.go
│   ├── room/        doc.go
│   ├── state/       doc.go
│   └── store/       doc.go
└── pkg/
    └── client/      doc.go
```
