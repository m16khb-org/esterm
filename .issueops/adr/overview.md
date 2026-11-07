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

## 방송 영상 분석 기록 (제거됨, 역사 기록)

방송 영상 분석(미니맵 위치 추적, 영상 동기화, 백테스트)은 제거되었다. 아래 기록은 그 시절의 결정이며 현재 구조를 설명하지 않는다. 제거 결정은 이 파일 맨 아래의 최신 기록을 본다.

- [챔피언 위치는 방송 미니맵 영상 CV로 얻는다](2026-10-18-cv.md)
- [동기화는 피드 시간축에서 HUD 시계와 킬 변화로 계속 재추정한다](2026-10-18-hud.md)
- [ffmpeg는 고정된 넓은 영역만 자르고 보정은 Go에서 한다](2026-10-18-ffmpeg-go.md)
- [과거 경기는 livestats 기록과 공식 VOD 를 같은 동기 엔진으로 재생한다](2026-10-18-livestats-vod.md)
- [챔피언 추적은 포트레이트 가설 + 전역 배정 + 칼만 필터로 하고, 피드와 메인 뷰를 함께 쓴다](2026-10-19-tracker.md)
- [Game-start gold skew prior and inverse-variance flip weighting](2026-10-19-game-start-skew-prior.md)
- [트래커 2차: 포트레이트는 CommunityDragon 원형 아이콘, 확실한 매치는 링 없이, 카메라 사각형 지우기, 먼 점프는 확인, 서포터 부착 상태](2026-10-19-tracker-round2.md)
- [메인 뷰 이름표는 글자로 읽지 않고 모양으로 묶어 투표로 챔피언에 잇는다](2026-10-19-nameplate-clusters.md)
- [놓친 트랙의 재획득은 트랙 게이트 안에서만 느슨한 기준을 쓴다; LPL 골드 시작 사전값은 넣지 않는다](2026-11-07-lpl.md)

## 현재 유효한 결정

- [Go 유지, Rust 전환 안 함](2026-10-18-go-rust.md)
- [기본 진입점을 lazygit 형태의 표준 라이브러리 TUI 앱으로 한다](2026-10-18-lazygit-tui.md)
- [용·엘더·바론 타이머는 피드의 처치 프레임에서 게임 시계로 계산한다](2026-10-18-objective-timers.md)
- [방송 영상 분석을 모두 제거하고 livestats 피드의 값만 보여 준다](2026-11-07-livestats.md)
