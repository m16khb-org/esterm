---
name: 2026-10-18-clock-stall-relock
description: Caution record for a solved false case or recurring risk.
---

# 방송 시계가 몇 초 멈췄다 따라잡으면 동기가 잘못 재잠금했다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 방송 시계가 6초 뒤처진 채 멈췄다가 빨리 따라잡는 동안 mmSync가 두 번 재잠금해 오프셋이 3초 동안 6.4초 틀어졌다.
- Context: 백테스트 KC-MVK g2에서 영상 1692~1695초에 HUD 시계가 1533에 멈춰 있다가 1696~1699초에 1536, 1540, 1543, 1544, 1545, 1546으로 따라잡았다(피드에 일시정지 없음). maybeRelock이 대기 표본 전체에서 값이 우연히 ref와 1초 안인 표본을 골라 3개, 2초 이상이면 재잠금했다. 멈춘 시계는 값이 계속 변해 흩어진 표본이 우연히 맞았다.
- Resolution: 재잠금에 쓰는 표본을 대기 표본의 최근 평평한 구간(오판독 2개까지 허용, syncRelockMiss)으로 제한했다(sync.go maybeRelock). 회귀 테스트: TestSyncSurvivesBroadcastClockStallAndCatchUp. 수정 뒤 같은 경기의 재잠금이 0회가 되었다.
