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
- 테스트 파일은 기능별로 있다: `feed_test.go`, `relay_test.go`, `replay_test.go`, `deaths_test.go`, `objectives_test.go`, `app_test.go`, `app_mouse_test.go`, `ui_*_test.go`, `i18n_test.go`, `main_test.go`. Windows 에서도 `go vet ./... && go test ./...`가 통과해야 한다.

## 이 저장소의 테스트 규칙

- 테스트는 오프라인에서 결정적으로 돌아야 한다. 네트워크와 `/tmp` 경로에 의존하지 않는다.
- 소비자가 체감하는 동작과 경계만 테스트한다. 예: 프레임 선택, 10초 창 정렬, 일시정지 처리, 사망·부활 계산, 오브젝트 타이머, 키·마우스 입력.
- 사망·부활 로직을 바꾸면 `deaths_test.go`의 단위 테스트를 함께 추가하거나 고친다.
- 테스트하지 않는 것: 실제 lolesports 네트워크 호출, 화면 픽셀 배치 전체, 구현 내부 상수.
- 일회용 스크립트는 `/tmp` 아래에 두고 검증 후 지운다.

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
