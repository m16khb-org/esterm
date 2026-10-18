---
name: 2026-10-18-livestats-align10-newest-0-5s
description: Caution record for a solved false case or recurring risk.
---

# livestats 창 프레임이 불규칙해 align10(newest+0.5s) 추종이 멈춘다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 창 경계에서 영원히 같은 창을 요청한다.
- Context: window 응답의 프레임 간격이 불규칙해 최신 프레임+0.5초를 10초로 내림한 startingTime을 쓰면 창 경계에서 진행하지 못했다.
- Resolution: 현재 창과 다음 창을 함께 폴링하도록 고쳤다(feed.go).
