---
name: 2026-10-18-lazygit-tui
description: Accepted decision record with rationale, alternatives, and consequences.
---

# 기본 진입점을 lazygit 형태의 표준 라이브러리 TUI 앱으로 한다

- Date: 2026-10-18
- Kind: `adr`
- Source: cli
- Summary: esterm(인자 없음)과 esterm lol 은 키보드로 리그·경기·세트·옵션을 고르는 패널형 앱을 연다.
- Context: 사용자 요청: 실행 방법이 복잡하니 방향키로 옵션을 골라 진행하고, TUI 를 lazygit 처럼 터미널에 최적화하라. 과거 경기 다시 보기(replay)도 같은 화면에서 열어야 했다. 의존성은 표준 라이브러리만 쓰는 원칙을 유지한다.
- Decision: ui_keys.go(키 디코더, 30ms ESC 판정), ui_widgets.go(둥근 테두리 패널, 목록, 힌트 바, 팝업, 메뉴), term_darwin/term_linux.go(raw 모드)로 자체 TUI 를 만들고, app.go 가 [1] Leagues [2] Matches [3] Games [0] 메인 패널, ',' 옵션, '?' 도움말을 구성한다. 보드는 buildCanvas 를 패널 크기로 그려 blit 하고 kitty 슬롯을 패널 원점만큼 옮긴다. 입력은 startKeys 하나로 통일했다. 기존 서브커맨드(relay, replay, minimap, schedule 등)는 스크립트용으로 유지한다.
- Consequences: 외부 TUI 라이브러리(bubbletea 등)는 표준 라이브러리 원칙 때문에 쓰지 않았다. 셀은 1칸 1글자라 한글 같은 2칸 문자는 정렬이 어긋나므로 UI 문자열은 ASCII/라틴으로 둔다. 옵션은 세션 동안만 유지된다. 근거: app.go, app_test.go, ui_*_test.go.
