---
name: overview
description: Family module overview: accepted architecture decisions and roadmap.
---

# Architecture Decision Records — Overview

Canonical index: [ADR.md](../ADR.md)

## Purpose

Record structural choices, rejected alternatives, and decisions that affect long-term maintenance. This is not an implementation note; preserve why this structure was chosen and which alternatives should not be retried.

## When to read

- Before architecture changes, large refactors, or dependency/framework replacement
- When changing or bypassing existing structure
- When modifying code whose historical rationale is unclear

## When to append

- A new structure or boundary was chosen.
- Alternatives were considered and rejection reasons will reduce future re-analysis.
- Operations, performance, or security constraints shaped the design.

## Entry template

### YYYY-MM-DD: <decision title>

- Context: <problem and constraints>
- Decision: <chosen structure>
- Alternatives rejected:
  - <alternative>: <why rejected>
- Consequences: <tradeoffs and follow-up>
- Evidence: <files, commands, issues, docs>
- [Go 유지, Rust 전환 안 함](2026-10-18-go-rust.md)
- [챔피언 위치는 방송 미니맵 영상 CV로 얻는다](2026-10-18-cv.md)
- [동기화는 피드 시간축에서 HUD 시계와 킬 변화로 계속 재추정한다](2026-10-18-hud.md)
- [ffmpeg는 고정된 넓은 영역만 자르고 보정은 Go에서 한다](2026-10-18-ffmpeg-go.md)
- [기본 진입점을 lazygit 형태의 표준 라이브러리 TUI 앱으로 한다](2026-10-18-lazygit-tui.md)
- [과거 경기는 livestats 기록과 공식 VOD 를 같은 동기 엔진으로 재생한다](2026-10-18-livestats-vod.md)
