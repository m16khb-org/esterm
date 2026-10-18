---
name: 2026-10-18-feed-backfill
description: Caution record for a solved false case or recurring risk.
---

# feed backfill 이 끝나지 않는 요청 폭주를 만들었다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: 재생 위치가 5분 버퍼보다 오래되면 backfill 이 매 틱 실패를 반복해 초당 5~10건의 요청을 보냈다.
- Context: feed.Need 의 중복 방지 조건(oldest.Equal(f.tried))은 버퍼 시작이 매 폴링 앞으로 밀려 맞지 않았고, keep 범위보다 오래된 구간은 받아도 바로 잘려 나갔다. 31분 장시간 시험(영상 켜기/끄기)에서 window 요청이 분당 244건, 30초 구간 최대 초당 10.6건까지 올라갔다.
- Resolution: keep 범위 밖 요청은 건너뛰고, 실패하면 2초부터 최대 2분까지 늘어나는 벽시계 기준 대기를 두었다(feed.go Need/backfill). 회귀 테스트: TestNeedDoesNotStormWhenNothingOlderExists, TestNeedSkipsTimeOlderThanTheBufferKeeps. 수정 후 같은 시험에서 분당 36건으로 기준선과 같았다.
