---
name: CONSTITUTION.md
description: Instruction priority, safety, and accuracy principles; read when rules conflict or an action is risky.
---

# Constitution

## SessionStart contract

This project-specific constitution should be read at session start. Follow the general LLM coding behavior guidelines at the top of AGENTS.md; this document adds harness structure, security, and verification invariants. Treat it as the baseline principle document for MCP routing.

## Source of truth

1. Latest explicit user/system instructions
2. Current repo AGENTS.md or a nearer nested AGENTS.md
3. .issueops/*.md
4. Current files and command output

## Principles

- Host adapters must not bypass core policy.
- Never put raw secrets in docs, logs, test fixtures, or MCP/CLI responses.
- Preserve explicit workspace-root and command-policy boundaries.
- Harness results observed from Codex and Claude Code should match.

## Complexity, readability, and consistency

**Prefer average or amortized O(1) data access and repeated hot-path operations when practical.**

- When alternatives do not materially harm performance or optimization value, prefer readability, established project conventions, and consistent APIs and structure.
- Do not add unnecessary caches, indexes, or duplicated state merely to claim O(1). Justify optimization with measured bottlenecks and time/space complexity evidence.
- Safety and correctness remain higher priorities when O(1) goals conflict.
