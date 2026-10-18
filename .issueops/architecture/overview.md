---
name: overview
description: Family module overview: dependency direction and runtime topology.
---

# Architecture — Overview

Canonical index: [ARCHITECTURE.md](../ARCHITECTURE.md)

## 구조 유형

단일 `package main` Go 바이너리다. 계층형이나 헥사고날 같은 이름 붙은 구조는 쓰지 않고, 파일 단위로 책임을 나눈 평면 구조다. 외부 경계(HTTP, 자식 프로세스, 터미널)만 함수·타입으로 분리되어 있다. Confidence: high (`go list` 기준 패키지는 `main` 하나).

## 진입점

- `main.go` `main` → `runLoL`이 `bench`, `demo`, `relay`, `minimap`, `live`, `streams`, `standings`, `schedule`, `leagues` 하위 명령으로 분기한다. HTTP 호출은 `getJSON`/`getJSONCtx`를 거친다.
- `relay.go` `cmdRelay`: 전체 화면 보드 루프(0.5초 렌더 틱). `buildCanvas`가 캔버스를 만들고 `renderDiff`가 이전 캔버스와의 차이만 ANSI로 출력한다. `relayPlayout.Tick`이 틱마다 보드가 보여 줄 게임 시각을 정한다.
- `commands.go`: `streams`, `standings` 등 조회 명령과 `streamURL`, `eventDetailsResp`. `panels.go`: `b`/`s`/`o` 토글 패널. `ddragon.go`: Data Dragon 이름·이미지(디스크 캐시). `staticdata.go`: 번들된 아이템·룬 이름. `tui.go`: darwin raw 모드.

## 데이터 흐름

1. 일정과 라이브 경기: lolesports `gw` API → `fetchLiveEvent`, `fetchSpawn`.
2. 실시간 통계: `feed.go`가 `feed.lolesports.com/livestats/v1/{window,details}/{gameId}`를 백그라운드로 동시에 가져오고, 10초 창 안의 0.5초 간격 프레임을 `Snapshot`/`SnapshotAt`으로 재생한다. 현재 창과 다음 창을 함께 폴링한다.
3. 미니맵 영상(선택): `yt-dlp`가 H.264 스트림 URL을 얻고 `ffmpeg`가 미니맵과 HUD 영역을 잘라 낸다(`minimap_video.go`, `mmSplitFrame`) → `minimap_layout.go`가 HUD 시계 글꼴로 방송 레이아웃(worlds/lck/lpl/lec)을 판별하고 `mmReadHUD`로 시계와 킬 수를 읽는다 → `minimap_fit.go`가 `map11` 상관 맞춤으로 지도 위치를 보정하고 `mmMapVisible`로 리플레이·드래프트 화면을 거른다 → `minimap_detect.go`가 챔피언 아이콘 템플릿 매칭과 추적으로 위치를 검출한다.
4. 동기화: `sync.go`의 `mmSync`가 HUD 시계와 피드 킬 이벤트로 피드↔방송 오프셋을 계속 다시 추정한다. `minimap.go`의 `mmPipeline.Process`가 위 단계를 이어 붙이고, `relayPlayout.syncedTick`이 잠금 상태일 때 피드 시각과 영상 시각을 맞춘다.
5. 그리기: `minimap_render.go`(점자 지도와 라벨), `minimap_kitty.go`(kitty 그래픽 아이콘). 공유 타입은 `minimap_types.go`.
6. 오브젝트 타이머: `objectives.go`가 피드 프레임의 dragons 목록과 barons 수 변화로 처치 이벤트를 만들고(`objDerive`) 보여 줄 프레임의 상태를 계산한다(`objAt`). 라이브는 `feed.objs`, replay 는 `replayStore.objs` 가 이벤트를 들고 있고, `feedSnap.Obj` → `mmView.Obj` 로 보드 줄(`objTimerRow`)과 미니맵(`objectives_render.go`, `objectives_kitty.go`)에 전달된다.

## 경계와 규칙

- 외부 프로세스는 `minimap_video.go`만 실행한다. 도구 경로는 `ESTERM_FFMPEG`, `ESTERM_YTDLP`로 바꾼다.
- 인증이 필요한 API는 없다. lolesports 공용 키를 쓴다(README.md).
- 영상 보정(지도 위치, 레이아웃, 시계 잠금, 오프셋)은 모두 Go에서 처리한다. 근거는 [ADR.md](../ADR.md)의 관련 기록을 본다.
- 리그별 HUD 지원 범위는 `mmLayouts`가 정한다. 그 밖의 방송 화면은 Unknown / not confirmed이며 `esterm lol minimap -src ... -n N -json`의 `layout` 필드로 확인한다.
