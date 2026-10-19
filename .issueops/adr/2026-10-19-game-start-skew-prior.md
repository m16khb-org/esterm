---
name: 2026-10-19-game-start-skew-prior
description: Accepted decision record with rationale, alternatives, and consequences.
---

# Game-start gold skew prior and inverse-variance flip weighting

- Date: 2026-10-19
- Kind: `adr`
- Source: cli
- Summary: 스큐 사전값은 피드의 첫 골드 시각 − 리그별 고정 간격으로 경기마다 잡고, 킬 플립은 사전값과 함께 역분산 가중 평균으로 섞는다. 새 입력 mmSync.ObserveGameStart는 피드가 경기 시작부터 있을 때만 쓴다.
- Context: 킬 플립이 학습되기 전의 첫 킬들은 리그 평균 스큐 사전값만으로 정렬되는데, 평균은 경기마다 ±0.5초 달라 킬이 적은 경기(15~38건)가 킬 플립 1초 이내 95%를 못 채웠다(cautions/2026-10-19-kill-flip-first-skew.md 참고). 피드 쪽 입력은 프레임뿐이라 새 입력이 필요했다.
- Decision: (1) gameStart(frames)가 골드 0 프레임 다음의 첫 골드 프레임(팀당 최대 3,000)을 경기 시작으로 본다. feed.go가 프레임을 받을 때 한 번 기억하고(feed.GameStart), replayStore도 같은 함수로 구하며, relay.go·replay.go·백테스트가 mmSync.ObserveGameStart로 넘긴다. (2) syncLayoutGap(worlds 0.85, lck 1.85, lec 0.98초)을 뺀 값이 그 경기의 스큐 사전값(σ 0.25초)이다. 간격이 없는 리그(lpl)와 시작을 못 본 경기는 리그 평균(σ 0.5초)을 쓴다. (3) 스큐는 사전값과 킬 플립 표본(σ 0.5초 + 카운터 틈/√12)의 역분산 가중 평균이고, 중앙값(킬 플립 1표, 사전값 1.5표)에서 max(1초, 3σ) 넘게 벗어난 표본은 뺀다. 새 값은 syncLayoutGap, syncFlipSigma, syncPriorSigma, syncStartSigma, syncPriorVotes이다.
- Consequences: NS-BFX g2 킬 플립 89.5%에서 97.4%, LEC g3 93.3%에서 100%, 나머지는 같거나 높고 잘못된 재잠금은 0이다. 라이브에서 경기 중간에 켜면 시작을 못 봐서 리그 평균으로 돌아간다. 간격 값은 worlds 4경기, lck 3경기, lec 2경기에서 잰 것이라 새 리그나 방송 설정이 바뀌면 다시 재야 한다(경기마다 첫 골드 시각과 학습된 스큐 중앙값의 차이). 근거: sync.go, sync_test.go, feed.go, replay_store.go.
