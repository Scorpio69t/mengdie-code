<div align="center">

<img src="./assets/brand/mengdie-mark.svg" alt="MengDie Code butterfly mark formed from code angle brackets" width="144">

# MengDie Code

**Remember correctly, not merely more.**

A local coding agent for Chinese developers, built around verifiable memory, evidence-driven reflection, China-friendly model providers, and first-class macOS and Windows support.

[中文](./README.md) · **English**

[![CI](https://github.com/Scorpio69t/mengdie-code/actions/workflows/ci.yml/badge.svg)](https://github.com/Scorpio69t/mengdie-code/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)

</div>

> [!IMPORTANT]
> MengDie Code is at the architecture and infrastructure stage. It is **not yet a production-ready or daily-usable coding agent**. The repository is public early so that real requirements, design reviews, and community feedback can shape it.

## What is MengDie Code?

Modern coding agents can read files, edit code, and run commands, but several daily problems remain:

- project conventions and rejected decisions must be repeated across sessions;
- long tasks lose goals and rationale after context compaction;
- incorrect memories are difficult to trace and correct;
- failed edits can be hard to rewind without polluting Git history;
- Chinese model providers, network conditions, and the many Chinese developers using MacBooks or Windows are rarely treated as one coherent first-class audience.

MengDie Code aims to be a local coding agent where **memories have provenance, conclusions have evidence, changes are reversible, and reflection remains reviewable**.

## Product direction

- **Verifiable memory** — provenance, scope, authority, validity, conflicts, and explicit `why/forget/export` controls.
- **Evidence-driven reflection** — offline analysis creates proposals by default; it does not silently rewrite code or project rules.
- **China-friendly providers** — DeepSeek, Kimi, Zhipu, and configurable OpenAI-compatible endpoints are early priorities.
- **First-class macOS and Windows support** — native terminal, shell, credential, path, process, and distribution behavior are designed and tested for both platforms.
- **Linux support** — Linux remains fully buildable and usable, while the first product-experience pass focuses on macOS and Windows.
- **Local-first Go binary** — a small distribution surface before any daemon or web frontend.

## Chinese-first open source

Chinese is the primary maintained language for documentation, issue templates, design discussions, and project announcements. English issues and pull requests are fully welcome; contributors are not required to speak Chinese.

This English README is the entry point for international readers. The detailed [architecture document](./ARCHITECTURE.md) is currently maintained in Chinese.

## Status and roadmap

- [x] Product positioning and v0.2 architecture blueprint
- [x] Chinese-first open-source repository infrastructure
- [x] Phase 1 Slice 01: five coding baselines plus config and app skeletons
- [x] Phase 1 Slice 02: versioned events, terminal/JSON Lines renderers, and Ctrl+C state machine
- [x] Phase 1 Slice 03: OpenAI-compatible HTTP/SSE, streamed tool-call assembly, and bounded retry ([protocol, Chinese](./docs/development/phase-1-slice-03/PROVIDER_PROTOCOL.md))
- [x] Phase 1 Slice 04: project-root path guard, Windows path semantics, and the Tool Prepare/Execute base protocol
- [x] Phase 1 Slice 05: read_file / list_files / search_text read-only tools with rg fallback and output limits
- [x] Phase 1 Slice 06: deterministic policy, interactive approval, and one-shot capabilities ([protocol, Chinese](./docs/development/phase-1-slice-06/POLICY_PROTOCOL.md))
- [x] Phase 1 Slice 07: exact edit/write tools with diff approval, root-anchored atomic writes, and TOCTOU guards ([protocol, Chinese](./docs/development/phase-1-slice-07/EDIT_WRITE_PROTOCOL.md))
- [x] Phase 1 Slice 08: controlled zsh/PowerShell execution with environment filtering, bounded output, and process-tree cancellation ([protocol, Chinese](./docs/development/phase-1-slice-08/SHELL_PROTOCOL.md))
- [x] Phase 1 Slice 09: single-Agent runtime, context building, run-scoped todos, and repetition guards ([protocol, Chinese](./docs/development/phase-1-slice-09/AGENT_RUNTIME_PROTOCOL.md))
- [x] Phase 1 Slice 10: structured Doctor, current DeepSeek/Kimi samples, and protected live Provider smoke ([notes, Chinese](./docs/development/phase-1-slice-10/DOCTOR_AND_SMOKE.md))
- [x] Phase 1 Slice 11A: one-shot interactive tasks, terminal approval loop, and fail-closed non-TTY behavior ([protocol, Chinese](./docs/development/phase-1-slice-11a/INTERACTIVE_RUNTIME.md))
- [x] Phase 1 Slice 11B: native smoke on three platforms plus four unsigned preview targets and SHA-256 ([preview guide, Chinese](./docs/development/phase-1-slice-11b/DEVELOPMENT_PREVIEW.md))
- [x] Phase 1 Slice 12: protected macOS/Windows live-Provider Coding preflight ([guide, Chinese](./docs/development/phase-1-slice-12/M1_EXIT_EVALUATION.md); DeepSeek passed 10/10 across both platforms)
- [x] Phase 2 Slice 02: SQLite EventStore, migration ledger, and a commit-before-output durability loop ([implementation report, Chinese](./docs/development/phase-2-slice-02/IMPLEMENTATION_REPORT.md))
- [x] Phase 2 Slice 03A: Command Ledger, pure Reducer/Snapshot, and session list/show/delete ([implementation report, Chinese](./docs/development/phase-2-slice-03a/IMPLEMENTATION_REPORT.md))
- [ ] M0: real-world coding, long-run, and memory-trust eval sets
- [ ] M1: minimum Agent Runtime capable of completing real tasks ([Phase 1 detailed design, Chinese](./docs/design/phase-1/DETAILED_DESIGN.md))
- [ ] M2: persistent events, resume, context compaction, and Patch Journal ([Phase 2 detailed design, Chinese](./docs/design/phase-2/DETAILED_DESIGN.md))
- [ ] M3: auditable, trustworthy memory
- [ ] M4: proposal-first reflection

See [ARCHITECTURE.md](./ARCHITECTURE.md) for the product architecture and the [Phase 1 detailed design](./docs/design/phase-1/DETAILED_DESIGN.md) for the implementation-ready M1 proposal. Both are currently maintained in Chinese. Dependency governance is documented in the Chinese-first [modern engineering guidelines](./docs/DEPENDENCIES.md), and the shared CLI/Web identity is covered by the [brand guide](./docs/BRAND.md).

## Local preview

The current source preview includes the CLI/app skeleton, layered configuration, a structured Doctor, five reproducible coding baselines, the Provider protocol, the safety toolchain, and a minimal Agent Runtime shared by `mengdie` and `mengdie exec`. Multi-platform previews, 20 consecutive successful main CI runs, and a 10/10 protected DeepSeek Coding preflight across macOS and Windows now have recorded evidence. M1 remains incomplete until external real-repository tasks and the security exit record are complete.

```bash
git clone https://github.com/Scorpio69t/mengdie-code.git
cd mengdie-code
go test ./...
go run ./cmd/mengdie --version
go run ./cmd/mengdie doctor --offline --json
go run ./cmd/mengdie doctor
go run ./cmd/mengdie
go run ./cmd/mengdie exec --json "inspect this project"
go run ./cmd/mengdie exec --json --command-id ci-job-42 "inspect this project"
go run ./cmd/mengdie exec --allow-edit --allow-command go,test "fix the failing test"
go run ./cmd/mengdie session list
go run ./cmd/mengdie session show --json <session-id>
go run ./cmd/mengdie session resume --message "continue checking" <session-id>
go run ./cmd/mengdie session delete --yes <session-id>
go run ./cmd/mengdie-eval --manifest evals/coding/smoke.json --pretty
```

The interactive entry accepts one task of at most 64 KiB per process. Reconstructable boundaries are committed to local SQLite before output, while streaming `message.delta` events remain transient. `session resume` creates a new Run in the same Session and restores complete user, assistant, read-only tool, and Todo boundaries; write, execute, and network results are replaced by recovery-safe summaries. Sessions without a context journal, pending approvals, incomplete tools, or mismatched private/public boundaries fail closed. Recoverable re-approval, retry of interrupted reads, Patch Journal, TUI, and REPL are not implemented yet; redirected input/output must use `mengdie exec`.

The default data directory is `~/Library/Application Support/MengDie Code/` on macOS, `%LOCALAPPDATA%\MengDie Code\` on Windows, and `$XDG_STATE_HOME/mengdie/` on Linux (falling back to `~/.local/state/mengdie/`). `MENGDIE_DATA_DIR` may override it, while repository-local, network-share, OneDrive/iCloud-synchronized, and symlink/reparse-point roots are rejected.

`exec --json` emits the complete run as JSON Lines. `--command-id` supplies an idempotency key: the same ID and task replay committed public facts without another Provider or tool call; different input conflicts. Running or interrupted work is never continued implicitly by `exec` and must pass explicit `session resume` analysis. Resume commands have their own idempotency key and replay only that recovery Run. Headless mode denies edit/write/shell by default; `--allow-edit`, a token-bounded `--allow-command`, and explicit `--allow-env NAME` grants are narrow run-scoped exceptions.

Public events, session projections, and logs exclude the full user task, credentials, and hidden reasoning. Idempotency and resume require full tasks and model-visible messages to be stored as private local SQLite facts; side-effecting tool results persist only safe summaries. These private facts rely on directory/file permissions and are not encrypted at rest. Do not place `MENGDIE_DATA_DIR` on shared or synchronized storage, and use `session delete --yes` to remove local sessions. API keys, allowed environment values, and replayable approval grants are never written to the ledger.

Go 1.26 or later is required.

`doctor --offline` performs local checks without constructing a Provider. The default command performs one bounded online tool-call probe with fixed content and no source code. Paths are represented by logical placeholders and credential values are never printed. See the [Chinese Doctor contract](./docs/development/phase-1-slice-10/DOCTOR_AND_SMOKE.md).

GitHub Actions produces seven-day unsigned previews for macOS Apple Silicon/Intel, Windows x64, and Linux x64, with SHA-256 checksums and build metadata. These are not formal releases; read the [Chinese preview, verification, and platform guide](./docs/development/phase-1-slice-11b/DEVELOPMENT_PREVIEW.md) before installing one.

Secret-free samples are available for [combined profiles](./configs/examples/config.toml), [DeepSeek](./configs/examples/deepseek.toml), [Kimi Code membership](./configs/examples/kimi-code.toml), and the [Kimi Open Platform](./configs/examples/kimi-platform.toml). Kimi Code and the Open Platform use separate keys, endpoints, and quotas, so their profiles must not be mixed. Provider model names and endpoints can change; each sample carries its verification date. API keys are referenced by environment-variable name and must not be stored in project configuration.

## Contributing

The most valuable early contributions are real coding-agent pain points, architecture review, reproducible evaluation scenarios, Chinese-provider compatibility, macOS and Windows execution safety, documentation, and focused milestone work.

Please read the Chinese-first [contribution guide](./CONTRIBUTING.md) and [code of conduct](./CODE_OF_CONDUCT.md). English contributions are welcome. Report security issues according to [SECURITY.md](./SECURITY.md).

## Name

MengDie means “Dreaming Butterfly,” inspired by Zhuangzi. The metaphor is simple: collaborate on code while awake, reflect while idle—but every learned behavior must remain evidence-based and human-reviewable.

## License

MengDie Code is licensed under the [Apache License 2.0](./LICENSE).
