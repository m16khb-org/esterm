---
name: 2026-10-18-renderdiff-sgr
description: Caution record for a solved false case or recurring risk.
---

# renderDiff에서 SGR 스타일이 초기화 없이 새어 나갔다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 스타일 리셋이 없으면 이후 셀에 색이 번진다.
- Context: relay.go의 renderDiff가 변경 셀만 출력하면서 이전 스타일을 끊지 않아 SGR 스타일이 다른 셀로 누출되었다.
- Resolution: 스타일이 바뀔 때 `writeStyle`(relay.go)이 `cReset`을 먼저 쓰고 새 스타일을 쓰도록 고쳤고, `renderDiff`와 `canvasString`이 이 함수를 함께 쓴다. 회귀 테스트는 `TestRenderDiffNoStyleLeak`(relay_test.go)로, 전체 다시 그리기와 차분 출력이 같은 화면을 만드는지 확인한다.
