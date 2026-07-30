<div align="center">

# 🦋 MengDie Code

**Remember correctly, not merely more.**

A local coding agent for Chinese developers, built around verifiable memory, evidence-driven reflection, China-friendly model providers, and first-class Windows support.

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
- Chinese model providers, network conditions, and Windows developers are rarely first-class priorities.

MengDie Code aims to be a local coding agent where **memories have provenance, conclusions have evidence, changes are reversible, and reflection remains reviewable**.

## Product direction

- **Verifiable memory** — provenance, scope, authority, validity, conflicts, and explicit `why/forget/export` controls.
- **Evidence-driven reflection** — offline analysis creates proposals by default; it does not silently rewrite code or project rules.
- **China-friendly providers** — DeepSeek, Kimi, Zhipu, and configurable OpenAI-compatible endpoints are early priorities.
- **First-class Windows support** — cross-platform behavior and security levels are described honestly.
- **Local-first Go binary** — a small distribution surface before any daemon or web frontend.

## Chinese-first open source

Chinese is the primary maintained language for documentation, issue templates, design discussions, and project announcements. English issues and pull requests are fully welcome; contributors are not required to speak Chinese.

This English README is the entry point for international readers. The detailed [architecture document](./ARCHITECTURE.md) is currently maintained in Chinese.

## Status and roadmap

- [x] Product positioning and v0.2 architecture blueprint
- [x] Chinese-first open-source repository infrastructure
- [ ] M0: real-world coding, long-run, and memory-trust eval sets
- [ ] M1: minimum Agent Runtime capable of completing real tasks
- [ ] M2: persistent events, resume, context compaction, and Patch Journal
- [ ] M3: auditable, trustworthy memory
- [ ] M4: proposal-first reflection

See [ARCHITECTURE.md](./ARCHITECTURE.md) for the complete design and milestone acceptance criteria.

## Local preview

The current command is only a compilable project placeholder for validating the repository and CI. It does not contain agent functionality yet.

```bash
git clone https://github.com/Scorpio69t/mengdie-code.git
cd mengdie-code
go test ./...
go run ./cmd/mengdie --version
```

Go 1.26 or later is required.

## Contributing

The most valuable early contributions are real coding-agent pain points, architecture review, reproducible evaluation scenarios, Chinese-provider compatibility, Windows execution safety, documentation, and focused milestone work.

Please read the Chinese-first [contribution guide](./CONTRIBUTING.md) and [code of conduct](./CODE_OF_CONDUCT.md). English contributions are welcome. Report security issues according to [SECURITY.md](./SECURITY.md).

## Name

MengDie means “Dreaming Butterfly,” inspired by Zhuangzi. The metaphor is simple: collaborate on code while awake, reflect while idle—but every learned behavior must remain evidence-based and human-reviewable.

## License

MengDie Code is licensed under the [Apache License 2.0](./LICENSE).

