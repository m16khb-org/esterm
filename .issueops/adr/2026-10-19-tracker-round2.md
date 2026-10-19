---
name: 2026-10-19-tracker-round2
description: Accepted decision record with rationale, alternatives, and consequences.
---

# 트래커 2차: 포트레이트는 CommunityDragon 원형 아이콘, 확실한 매치는 링 없이, 카메라 사각형 지우기, 먼 점프는 확인, 서포터 부착 상태

- Date: 2026-10-19
- Kind: `adr`
- Source: cli
- Summary: LPL 생존 검출 84.4%에서 90.9%(g1), 서포터 Yuumi 62.5%에서 93.3%(LEC g1), LCK GEN-HLE g3 88.8%에서 92.4%. 다섯 가지를 바꿨다.
- Context: LPL g1의 손실을 챔피언별로 분해했다. 지도 맞춤은 문제가 아니었다(8,034 추적 프레임 모두 통상 박스에서 2 px 안). 손실의 대부분은 (1) 팀 링 검사: 전체 지도 후보 중 점수 0.6 이상인 것이 링 기울기 4 미만이라 버려진 비율이 살아 있는 챔피언 프레임의 7.6%였다. LPL의 링은 얇고 어두워 팀 색으로 거의 기울지 않는다(청팀 r=10px에서 (52,52,69)). K'Sante의 실제 아이콘이 정확히 이 때문에 0.8 넘는 점수로도 버려졌다. (2) 템플릿: Data Dragon 정사각형이 미니맵 아이콘과 다른 크롭인 챔피언이 있다(Qiyana 0.61, 원형 아이콘 0.82; K'Sante 0.84와 0.88). (3) 방송이 아이콘 위에 그리는 흰 카메라 사각형 선이 아이콘을 가로지르면 점수가 떨어진다(Jarvan). (4) 같은 팀 아이콘끼리의 한 프레임짜리 오매칭이 좌표를 튀게 한다. (5) 서포터(Yuumi)는 아군에 붙으면 지도에서 사라지는데, 붙기 전에 한 번도 못 잡으면 가림 추정에 들어가지 못했다.
- Decision: (1) 템플릿은 CommunityDragon의 미니맵 원형 아이콘(characters/<id>/hud/<id>_circle_0.png)을 쓰고 없으면 Data Dragon 정사각형으로 물러난다(minimap_icons.go). (2) 점수 0.8 이상(mmSureScore) 매치는 링 검사 없이 받되 색 일치(mmSureColour 0.3, 아주 틀린 색만 거른다. 0.6은 LPL g1을 89.9%로 낮췄다)는 본다. 전체 지도 후보 4,400개 중 0.8 이상의 오검출은 18개뿐이었고 그 아래는 2만 5천 개였다. (3) 카메라 사각형의 20 px 넘는 곧은 흰 선을 이웃 화소로 메워 매칭에 넣는다(minimap_viewport.go). (4) 방금(1초 안) 본 챔피언이 허용 반경 1.5배 밖에서 나타나면 다음 프레임에도 같은 곳에 있어야 받는다(순간이동은 1프레임 늦다). (5) Yuumi처럼 아군에 붙는 챔피언(mmAttachers)이 살아 있는데 3초 넘게 지도에 없으면 부착으로 본다. 대상은 직전 대상, 아니면 같은 팀 바텀, 아니면 마지막 위치에서 가장 가까운 아군이다. 부착 중에는 대상의 추정 위치와 나이를 따르고 아이콘이 다시 잡히거나 대상이 죽으면 풀린다(mmDet.Attached).
- Consequences: 기각한 것: 스킨별 원형 아이콘 전수 시험(이번 경기의 위약 챔피언은 모두 기본 스킨이었고 circle_0이 가장 높았다. 스킨 선택은 하지 않았다). 링 검사 삭제(0.6~0.8 구간 후보는 거의 전부 오검출이다). 부착의 정답 위치는 없다: 아이콘이 지도에 없다는 사실과 직전 대상 또는 바텀이라는 사전값이 근거라 틀릴 수 있고, 지표에는 covered로 센다. 점프 확인은 같은 팀 아이콘끼리의 오매칭(측정된 점프, 오검출 대리 지표)을 LPL g1 55건에서 36건으로 줄였지만 점수 0.9대의 확정된 점프가 남는다. 근거: minimap_icons.go, minimap_viewport.go, minimap_detect.go(plausible), minimap_track.go(assign, markCovered), backtest_metrics_test.go(Jumps, FitOff).
