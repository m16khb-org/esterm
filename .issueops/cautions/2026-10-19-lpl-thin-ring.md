---
name: 2026-10-19-lpl-thin-ring
description: Caution record for a solved false case or recurring risk.
---

# LPL의 얇은 팀 링과 카메라 사각형 선이 정상 아이콘을 버리게 했다

- Date: 2026-10-19
- Kind: `caution`
- Source: cli
- Summary: LPL 생존 검출 손실의 주원인은 지도 맞춤이 아니라 링 기울기 검사였다. 방송마다 링이 다르면 링 검사가 정상 아이콘을 버린다.
- Context: LPL g1에서 점수 0.6 이상 후보가 링 기울기 4 미만으로 버려진 프레임이 살아 있는 챔피언 프레임의 7.6%, 색 검사로 버려진 것이 3.0%였다. 지도 맞춤이 통상 박스와 2 px 넘게 어긋난 프레임은 0개였다(장면 전환 가설은 이 경기에서 사실이 아니었다). Qiyana는 Data Dragon 정사각형과 점수 0.61로 평균 66%에 머물렀다.
- Resolution: 점수 0.8 이상 매치는 링 없이 받는다(색은 본다). 템플릿은 CommunityDragon 원형 아이콘이다. 새 리그나 방송을 붙일 때는 ESTERM_BACKTEST_MISS=1로 후보를 링 기울기와 점수별로 나눠 보고 링 검사가 정상 아이콘을 버리는지 먼저 본다. 지도 맞춤 가드는 이 경기에서 필요 없었으므로 만들지 않았다.
