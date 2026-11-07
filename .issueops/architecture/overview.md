---
name: overview
description: Family module overview: dependency direction and runtime topology.
---

# Architecture — Overview

Canonical index: [ARCHITECTURE.md](../ARCHITECTURE.md)

## 구조 유형

단일 `package main` Go 바이너리다. 계층형이나 헥사고날 같은 이름 붙은 구조는 쓰지 않고, 파일 단위로 책임을 나눈 평면 구조다. 외부 경계(HTTP, 터미널)만 함수·타입으로 분리되어 있다. Confidence: high (`go list` 기준 패키지는 `main` 하나).

## 진입점

- `main.go` `main` → `runLoL`이 `bench`, `demo`, `relay`, `replay`, `live`, `streams`, `standings`, `schedule`, `leagues` 하위 명령으로 분기한다. HTTP 호출은 `getJSON`/`getJSONCtx`를 거친다.
- `relay.go` `cmdRelay`: 전체 화면 보드 루프(0.5초 렌더 틱). `buildCanvas`가 캔버스를 만들고 `renderDiff`가 이전 캔버스와의 차이만 ANSI로 출력한다. `relayPlayout.Tick`이 틱마다 보드가 보여 줄 게임 시각을 정한다.
- `commands.go`: `streams`, `standings` 등 조회 명령과 `streamURL`, `eventDetailsResp`. `panels.go`: `b`/`s`/`o` 토글 패널. `staticdata.go`: 번들된 아이템·룬 이름(네트워크에서 받지 않는다). 터미널 크기·raw 모드는 `term_unix.go`·`term_darwin.go`·`term_linux.go`·`term_windows.go`.

## 데이터 흐름

1. 일정과 라이브 경기: lolesports `gw` API → `fetchLiveEvent`, `fetchSpawn`. 팀 순서는 `getLive`가 시리즈마다 고정하므로 블루 진영은 피드의 `blueTeamMetadata.esportsTeamId`가 정한다.
2. 실시간 통계: `feed.go`가 `feed.lolesports.com/livestats/v1/{window,details}/{gameId}`를 백그라운드로 동시에 가져오고, 10초 창 안의 0.5초 간격 프레임을 `Snapshot`으로 재생한다. 현재 창과 다음 창을 함께 폴링한다. 재생 지연은 최근 수신 지연에서 정한다(`feed.delay`).
3. 게임 시계: `feed.gameTime`이 스폰 이후 피드 시각에서 고정 시차(`feedSkew`)와 일시정지(`feedPause`)를 뺀다. 일시정지는 `findPauses`가 paused 프레임 뒤의 빈 구간에서 찾고, 늦게 연 보드는 `scanHistory`가 버퍼 이전의 10초 창을 읽어 보충한다.
4. 사망·부활: `deaths.go`의 `livesAt`이 deaths 카운트와 체력으로 사망과 부활을 읽는다. 라이브는 레벨·게임 시간 공식(`deathTimer`)으로 부활을 추정하고, replay는 이후 프레임에서 정확한 부활을 읽는다.
5. replay: `replay.go`가 보드를 만들고 `replay_store.go`의 `replayStore`가 10초 창 단위로 게임 전체 프레임을 한 번씩만 받아 들고 있다(최근 3게임 유지).
6. 오브젝트 타이머: `objectives.go`가 피드 프레임의 dragons 목록과 barons 수 변화로 처치 이벤트를 만들고(`objDerive`) 보여 줄 프레임의 상태를 계산한다(`objAt`). 라이브는 `feed.objs`, replay 는 `replayStore.objs` 가 이벤트를 들고 있고, `feedSnap.Obj` → `boardView.Obj` 로 보드 줄(`objTimerRow`)에 전달된다.
7. 대화형 앱: `app.go`가 목록 창과 `[0]` 보드 창, 옵션 메뉴를 구성하고 `ui_keys.go`·`ui_mouse.go`·`ui_widgets.go`가 키·마우스·위젯을 맡는다. 문구는 `i18n.go`가 한국어·영어·중국어로 준다.

## 경계와 규칙

- 외부 프로세스는 실행하지 않는다. 필요한 외부 도구는 없다.
- 인증이 필요한 API는 없다. lolesports 공용 키를 쓴다(README.md).
- 보드의 값은 모두 livestats 피드에서 온다. 피드에 없는 값(챔피언 위치, 텔레포트·귀환, 밴픽)은 보여 주지 않는다. 근거는 [ADR.md](../ADR.md)의 관련 기록을 본다.
