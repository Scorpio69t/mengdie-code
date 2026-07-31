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
- [ ] M0: real-world coding, long-run, and memory-trust eval sets
- [ ] M1: minimum Agent Runtime capable of completing real tasks ([Phase 1 detailed design, Chinese](./docs/design/phase-1/DETAILED_DESIGN.md))
- [ ] M2: persistent events, resume, context compaction, and Patch Journal
- [ ] M3: auditable, trustworthy memory
- [ ] M4: proposal-first reflection

See [ARCHITECTURE.md](./ARCHITECTURE.md) for the product architecture and the [Phase 1 detailed design](./docs/design/phase-1/DETAILED_DESIGN.md) for the implementation-ready M1 proposal. Both are currently maintained in Chinese. Dependency governance is documented in the Chinese-first [modern engineering guidelines](./docs/DEPENDENCIES.md), and the shared CLI/Web identity is covered by the [brand guide](./docs/BRAND.md).

## Local preview

The current preview includes the CLI/app skeleton, layered configuration, a minimal doctor command, five reproducible coding baselines, the event/rendering boundary, and an offline-tested Provider protocol layer. The Provider is not wired into `mengdie exec` yet; the Agent Runtime and real-model smoke tests remain follow-up work.

```bash
git clone https://github.com/Scorpio69t/mengdie-code.git
cd mengdie-code
go test ./...
go run ./cmd/mengdie --version
go run ./cmd/mengdie doctor --json
go run ./cmd/mengdie exec --json "inspect this project"
go run ./cmd/mengdie-eval --manifest evals/coding/smoke.json --pretty
```

For now, `exec --json` emits `run.started` and `run.failed` JSON Lines and exits with code 1. This previews the pipeline contract without pretending that the task ran. Events exclude the full user task, credentials, and hidden reasoning. Human-readable events use stderr; JSON Lines use stdout.

Go 1.26 or later is required.

A secret-free domestic-provider example is available at [`configs/examples/config.toml`](./configs/examples/config.toml). API keys are referenced by environment-variable name and must not be stored in project configuration.

## Contributing

The most valuable early contributions are real coding-agent pain points, architecture review, reproducible evaluation scenarios, Chinese-provider compatibility, macOS and Windows execution safety, documentation, and focused milestone work.

Please read the Chinese-first [contribution guide](./CONTRIBUTING.md) and [code of conduct](./CODE_OF_CONDUCT.md). English contributions are welcome. Report security issues according to [SECURITY.md](./SECURITY.md).

## Name

MengDie means “Dreaming Butterfly,” inspired by Zhuangzi. The metaphor is simple: collaborate on code while awake, reflect while idle—but every learned behavior must remain evidence-based and human-reviewable.

## License

MengDie Code is licensed under the [Apache License 2.0](./LICENSE).
