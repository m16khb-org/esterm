---
name: overview
description: Family module overview: test strategy and verification gates.
---

# Testing — Overview

Canonical index: [TESTING.md](../TESTING.md)

## Purpose

This is the reference document agents should read before writing or modifying tests. It records candidate commands and the difference between well-structured and poorly structured tests.

## When to read

- When writing tests for a new feature or bug fix
- When existing tests fail or are flaky
- When proving behavior preservation after refactoring
- When deciding which verification to run before completion

## 이 저장소의 검증 명령

- `go build ./... && go vet ./... && go test -race ./...` (go.mod, 모든 `*_test.go`). 변경 후 이 세 가지를 통과해야 한다.
- 테스트 파일은 기능별로 있다: `feed_test.go`, `relay_test.go`, `sync_test.go`, `minimap_*_test.go`.

## 이 저장소의 테스트 규칙

- 테스트는 오프라인에서 결정적으로 돌아야 한다. 네트워크, `/tmp` 경로, 방송 프레임 파일에 의존하지 않는다.
- 방송 화면 캡처를 저장소에 넣지 않는다. 학습된 글리프 템플릿처럼 작은 숫자 배열은 코드(`minimap_layout_data.go`)에 내장할 수 있다.
- 소비자가 체감하는 동작과 경계만 테스트한다. 예: 프레임 선택, 10초 창 정렬, 추적기의 숨김/재획득, 동기화 잠금과 일시정지 처리.
- 테스트하지 않는 것: 실제 YouTube/ffmpeg 호출, 화면 픽셀 배치 전체, 구현 내부 상수.

## VOD 에뮬레이션 하네스

실제 방송으로 하는 검증은 영구 테스트가 아니라 `/tmp` 아래의 일회용 하네스로 한다.

- 완료된 경기의 VOD를 `esterm lol minimap -src <VOD URL> -ss <초> -game <gameId> -n N -json`으로 돌려 HUD 시계와 검출 결과를 확인한다.
- 정답 비교는 VOD 초와 HUD 시계의 관계, 피드 킬 시각으로 만든다. 일회용 스크립트와 프레임은 검증 후 지운다.
- 새 리그나 레이아웃을 추가하면 그 리그의 시계 판독률과 오판 수를 같은 방식으로 측정해 보고한다. 측정 없이 지원한다고 쓰지 않는다.

## Well-structured tests

- Directly verify changed behavior and prefer public contracts/observable behavior over implementation details.
- Use assertion messages or fixture names that reveal the failure cause.
- Keep tests deterministic, independently runnable, and not overly dependent on order, time, external network, or global state.
- Regression tests clearly encode the recurring input/context and expected result.
- Reuse existing test style/helpers and explain intent/scope for broad snapshot or golden updates.

## Poorly-structured tests

- Locking only internal implementation structure and blocking harmless refactors.
- Adding assertions not tied to a real bug or requirement.
- Depending on sleeps, real network, local machine state, or test order.
- Using huge fixtures or vague snapshots that do not explain failures.
- Weakening production behavior just to pass tests.

## Rule

Before running, check README and this document for more specific instructions. When adding tests, apply the well-structured/poorly-structured criteria above first.
