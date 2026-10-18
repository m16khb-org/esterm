---
name: 2026-10-18-lpl-replay-skew
description: Caution record for a solved false case or recurring risk.
---

# LPL 방송은 리플레이 뒤 영상-피드 시차가 약 4초 바뀐다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: LPL 영어 방송에서 킬 리플레이가 끝나 라이브로 돌아오면 HUD 시계가 4초 앞서, 영상-피드 오프셋이 단계로 바뀐다.
- Context: 백테스트 LPL 3경기(결승 AL-BLG)에서 재잠금이 각 1회 있었고 모두 게임 시계 약 19:20~19:40, 'TCL REPLAY' 화면과 LPL 전환 화면 직후였다. 시계가 약 2초 읽히지 않다가 4초 앞선 값으로 돌아왔고(g1 오프셋 -47.70 -> -43.63), 킬 플립이 같은 크기(4.1초)만큼 움직여 재잠금이 옳음을 확인했다. 또한 LPL은 경기마다 피드-HUD 시계 차이가 달라(g3 +1.4초, g4 -1.1초 사전값 오차) 사전값 3.3초로는 첫 킬 몇 개가 1초를 넘게 어긋난다(LPL 킬 플립 1초 이내 86.7%).
- Resolution: 재잠금은 오류가 아니므로 동기 추정기를 바꾸지 않았다. 백테스트는 킬 플립이 재잠금의 이동량을 확인하면 옳은 재잠금으로 센다(btRelockTol). 첫 킬 몇 개의 오차를 줄이려면 사전값 가중(syncPriorWeight)을 이 백테스트로 다시 재야 한다.
