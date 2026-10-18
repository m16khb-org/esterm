---
name: 2026-10-18-livestats-vod
description: Accepted decision record with rationale, alternatives, and consequences.
---

# 과거 경기는 livestats 기록과 공식 VOD 를 같은 동기 엔진으로 재생한다

- Date: 2026-10-18
- Kind: `adr`
- Source: cli
- Summary: replay 는 끝난 경기의 피드 창을 위치 기준으로 받아 재생하고, 공식 VOD 미니맵을 HUD 시계와 킬 변화로 맞춘다.
- Context: getEventDetails 가 세트별 VOD(YouTube 등)와 startMillis/firstFrameTime 을 주지만 VOD↔게임 시각 추정 오차가 리그마다 달랐다(Worlds +6.8초, LCK -0.6초, LEC +343초).
- Decision: replay.go/replay_store.go/replay_video.go: 위치 시계(일시정지, ±10초 탐색, 1/2/4배속)를 두고, 피드 창은 위치 주변부터 받고 나머지는 초당 4건 이하로 채우며 최근 3경기를 캐시한다. 영상은 추정 위치보다 30초 앞의 탐색 구간에서 HUD 시계를 찾아 동기를 잡은 뒤 실제 위치로 다시 탐색하고, 배속은 ffmpeg -readrate, 일시정지는 파이프 역압으로 처리한다. VOD 가 없으면 점수판만 재생한다.
- Consequences: 탐색 후 미니맵 복귀에 5~11초가 걸린다. 앞선 세트의 시리즈 점수는 API 가 최종 점수만 줘서 [-] 로 표시한다. 근거: replay_test.go, 수동 pty 검증.
