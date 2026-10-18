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
- 테스트 파일은 기능별로 있다: `feed_test.go`, `relay_test.go`, `sync_test.go`, `minimap_*_test.go`, `objectives*_test.go`.

## 이 저장소의 테스트 규칙

- 테스트는 오프라인에서 결정적으로 돌아야 한다. 네트워크, `/tmp` 경로, 방송 프레임 파일에 의존하지 않는다.
- 방송 화면 캡처를 저장소에 넣지 않는다. 학습된 글리프 템플릿처럼 작은 숫자 배열은 코드(`minimap_layout_data.go`)에 내장할 수 있다.
- 소비자가 체감하는 동작과 경계만 테스트한다. 예: 프레임 선택, 10초 창 정렬, 추적기의 숨김/재획득, 동기화 잠금과 일시정지 처리.
- 테스트하지 않는 것: 실제 YouTube/ffmpeg 호출, 화면 픽셀 배치 전체, 구현 내부 상수.

## VOD 에뮬레이션 하네스

실제 방송으로 하는 검증은 일반 `go test`에 넣지 않고, 빌드 태그 `backtest`가 붙은 영구 백테스트(`backtest_test.go`, `backtest_run_test.go`, `backtest_metrics_test.go`)로 한다.

- 실행: `go test -tags backtest -run TestBacktest -timeout 3h -v . -backtest.out=결과.json`. `ESTERM_BACKTEST_GAMES=<정규식>` 으로 경기를 고르고 `ESTERM_BACKTEST_RANGE=시작-끝`(경기 시작부터 초)으로 구간만 돌리고(빠른 반복용) `ESTERM_BACKTEST_NOMV=1` 로 메인 뷰를 끄고 `ESTERM_BACKTEST_DOWNLOAD_ONLY=1` 로 영상 캐시만 채우고 `ESTERM_BACKTEST_V=1` 로 어긋난 킬 플립·깜빡임·재잠금을 모두 본다. 영상과 피드 응답은 `os.UserCacheDir()/esterm/backtest`(약 19 GB)에 캐시한다.
- 경기 목록(`btGames`)의 공식 완료 16경기(Worlds 2026 7, LCK 3, LPL 3, LEC 3)를 앱과 같은 `mmPipeline`·`mmSync`·`deaths.go`로 영상 전체에 돌리고, 피드를 정답으로 동기 오차, 검출률, 부활 오차, 깜빡임, 공백 원인을 잰다. 기준과 기준선 수치는 README 백테스트 절에 있다.
- 트래커, 동기, HUD, 사망 로직을 바꾸면 변경 전후 `-backtest.out` JSON을 비교해 보고한다. 새 리그나 레이아웃을 추가하면 그 경기를 `btGames`에 넣고(API에 VOD가 없으면 `Vod`와 `At` 지정) 같은 지표를 잰다. 측정 없이 지원한다고 쓰지 않는다.
- 일회용 스크립트와 프레임은 `/tmp` 아래에 두고 검증 후 지운다. 저장소에는 방송 화면을 넣지 않는다.

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
