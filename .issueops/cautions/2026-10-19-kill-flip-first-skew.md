---
name: 2026-10-19-kill-flip-first-skew
description: Caution record for a solved false case or recurring risk.
---

# 킬 플립 첫 몇 건이 어긋난 이유: 스큐 사전값이 경기마다 ±0.5초 달랐다

- Date: 2026-10-19
- Kind: `caution`
- Source: cli
- Summary: 백테스트가 킬 플립을 반영 직전 오프셋으로 재므로 첫 킬은 사전값만으로 정해진다. 리그 평균 스큐 사전값은 경기마다 ±0.5초 다르고 킬 플립 하나의 오차(±0.5초)가 더해져 NS-BFX g2·LEC g3처럼 킬이 적은 경기가 95%를 못 채웠다. 피드의 첫 골드 시각으로 경기별 스큐를 ±0.15초에 잡고 킬 플립을 역분산 가중으로 섞어 해결했다.
- Context: 측정은 btMeasure가 킬 플립 직전 프레임의 발행 오프셋과 피드 킬 시각의 차이로 한다(킬이 반영되기 전 값). 그래서 첫 킬은 사전값(리그 평균 스큐)만으로, 둘째부터는 앞선 킬 플립으로 배운 스큐로 결정된다. 옛 추정기는 사전값을 킬 3건으로 쳐서 섞고 중앙값에서 1초 넘게 벗어난 표본을 버렸다. 그래서 사전값에서 1초 넘게 먼 킬 플립은 세 건이 쌓일 때까지 무시돼 NS-BFX g2는 첫 세 킬이 +1.42/-1.41/+1.14초, LEC g3는 첫 킬이 -1.11초로 1초를 넘겼다(킬이 38건과 15건이라 한 건이 5%를 넘는다). 킬 플립 하나가 스큐를 알려 주는 정밀도는 약 0.5초(피드 프레임 시각과 HUD 카운터 지연)이고 리그 평균은 같은 리그 안에서도 0.3~0.5초 달랐다. 한편 피드가 팀 골드를 처음 보여 준 시각(게임 시작, 팀당 2,500)에서 스큐를 뺀 간격은 리그 안에서 거의 같았다: lck 1.84/1.83/약 1.8(GEN-HLE g1·g3, NS-BFX), lec 0.96/1.00(G2-MKOI g1·g3), worlds 0.92/0.88/0.84/0.76(MVK-FUR g2, FUR-C9 g1, MVK-FUR g1, KC-C9 g1). 반면 lpl은 간격이 일정하지 않았다(g1 리플레이 전 약 2.8초, 후 1.5초, g3 리플레이 전 0.8~2.4초로 표류, 후 1.6초).
- Resolution: sync.go: 스큐 추정을 사전값 가중 3건 + 중앙값 게이트에서 역분산 가중 평균으로 바꿨다(skewLocked). 킬 플립 표본의 잡음은 syncFlipSigma 0.5초에 HUD 카운터를 읽은 틈(너비/√12)을 더하고, 사전값은 syncPriorSigma 0.35초(리그 평균) 또는 syncStartSigma 0.25초(첫 골드 시각 - syncLayoutGap)이다. 중앙값은 킬 플립마다 1표, 사전값에 syncPriorVotes 1.5표를 줘서 구하고, 그 중앙값에서 max(1초, 3σ) 넘게 벗어난 표본은 평균에서 뺀다(사전값을 거스르는 킬 하나는 버려지고 둘이 모이면 사전값이 밀린다). 첫 골드 시각은 gameStart(프레임에서 골드 0 뒤 첫 골드 프레임)가 구하고 mmSync.ObserveGameStart로 들어온다(replay.go, relay.go의 feed.GameStart, 백테스트). 피드가 경기 시작부터 있을 때만 알 수 있어서 중간에 켠 라이브와 lpl은 리그 평균을 쓴다. 되돌리지 말 것: 킬 플립 하나를 사전값 3건으로 눌러 버리는 가중. 한계: 킬 플립 하나의 오차 ±0.5초는 줄지 않아서 완벽한 스큐라도 약 2~3%는 1초를 넘고, 킬이 15~25건인 경기는 한 건으로 95%가 깨질 수 있다. lpl은 리플레이 전 구간에서 피드-영상 차이가 방송 시계와 따로 표류해(g3 4.0초에서 2.4초로 내림) 87~92%에 머문다. 회귀 테스트: TestSyncSkewFarFromPriorIsLearnedFromTheFirstFlips, TestSyncSkewIgnoresAMismatchedKill, TestSyncGameStartPlacesTheSkewBeforeAnyFlip, TestGameStartIsTheFirstGoldAfterEmptyFrames.
