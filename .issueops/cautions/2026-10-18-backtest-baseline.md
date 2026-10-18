---
name: 2026-10-18-backtest-baseline
description: Caution record for a solved false case or recurring risk.
---

# 백테스트 기준선: 트래커 검출 76%, 사망 오검출 9%, 5초 넘는 마커 공백 21%

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 공식 16경기 전체 VOD 백테스트에서 생존 검출 75.9%, 사망 오검출 8.7%, 살아 있는 챔피언 시간의 21%가 5초 넘게 마커 없음 또는 흐림이었다.
- Context: backtest_test.go(빌드 태그 backtest)로 Worlds 7, LCK 3, LPL 3, LEC 3경기(491.8분)를 앱과 같은 mmPipeline·mmSync로 돌리고 피드를 정답으로 비교했다. 생존 챔피언 검출 64.7~83.4%(겹침 없는 챔피언 79.3%, 겹친 챔피언 68.5%), 사망한 챔피언을 찾았다고 한 비율 0.8~25.4%(1,666건 중 79%는 팀 분수대가 아닌 지도 곳곳). 5초 넘는 공백 3,107건: 다른 아이콘 옆에서 놓침 1,179건 19,860 챔피언·초, 빈 곳에서 놓침 864건 14,370초, 방송에 미니맵이 없음 818건 23,969초, 오브젝트 배너 189건 1,958초. 최장 공백 202.5초(90초 보관 만료). 3초 넘는 미니맵 숨김 90건은 모두 HUD 판이 사라진 진짜 숨김이었고 가짜 숨김은 0건이다. 사망 직전 마지막 추적 위치와 사망 중 표시 위치(기지)의 거리는 중앙값 지도의 0.47배다.
- Resolution: 트래커 개편의 기준선이다. 같은 명령(go test -tags backtest -run TestBacktest -timeout 3h -v . -backtest.out=파일)을 개편 후에 돌려 두 JSON을 비교한다. 기준선 수치는 README 백테스트 절에 있다.
