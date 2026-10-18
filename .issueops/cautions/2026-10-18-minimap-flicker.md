---
name: 2026-10-18-minimap-flicker
description: Caution record for a solved false case or recurring risk.
---

# 미니맵 챔피언 아이콘이 생겼다 없어졌다 한다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 실제 경기 구간에서 지도 표시가 분당 약 1회 꺼지고, 챔피언 아이콘이 분당 12~18번 사라졌다 다시 나타났다.
- Context: Worlds 2026 플레이인 MVK vs FUR(게임 1·3), LCK, LEC VOD를 mmPipeline.Process로 끝까지 돌려 측정했다(각 10~23분). 원인은 세 가지였다. (1) 오브젝트 배너(드래곤 처치 등)나 LEC의 미니맵 확대가 지도를 3~5초 가리면 지도 맞춤이 실패해 지도 전체가 숨겨졌다. (2) HUD 시계 글자가 1~4초 동안 읽히지 않아 시계 신선도(1초) 판정이 지도를 껐다. (3) 추적기가 챔피언을 프레임마다 약 25% 놓쳤고, 놓친 프레임마다 kitty 스프라이트가 지워졌다 다시 놓였다. 리플레이와 데스크 화면은 HUD 시계가 20초 이상 사라지는 것으로 구분됐다.
- Resolution: minimap.go의 mmPipeline에 세 층을 두었다. HUD 판정은 시계를 마지막으로 읽은 HUD의 윤곽 화소(글자, 로고, 선)가 아직 화면에 있으면 mmClockLong(10초)까지 유지하고, 윤곽이 사라지면 즉시 끈다(hudLive, setPlate). 배너가 가리는 동안은 HUD가 살아 있으면 mmCoverMax(10초)까지 마지막 위치를 유지한다. 추적기가 놓친 챔피언은 mmHoldMax까지 마지막 위치에 두고 mmDet.Age로 나이를 알린다. 피드가 살아 있다고 말하는 챔피언은 mmStaleAfter(2초) 뒤부터 흐리게(est.) 그리고 피드가 죽었다고 말하는 챔피언은 기지에서 부활 시간과 함께 그린다(mmChampsOf). 플레이트 판정은 윤곽 화소만 쓴다: 전체 화소 비교는 LCK 리플레이의 어두운 HUD 영역과 라이브 플레이트를 구분하지 못해 3프레임을 새게 했다. 방송 화면 판정을 바꾸면 worlds, lck, lec VOD의 리플레이 구간(HUD 없음)과 시계가 안 읽히는 라이브 구간으로 같은 측정을 다시 한다. LPL은 공식 VOD가 API에 없어 측정하지 못했다.
