---
name: 2026-11-07-livestats
description: Accepted decision record with rationale, alternatives, and consequences.
---

# 방송 영상 분석을 모두 제거하고 livestats 피드의 값만 보여 준다

- Date: 2026-11-07
- Kind: `adr`
- Source: cli
- Summary: 방송 영상에서 추정하던 챔피언 위치·텔레포트·귀환·메인 뷰 체력바·영상 동기를 모두 제거하고, 보드는 livestats 피드의 정확한 값만 보여 준다.
- Context: 사용자가 실제 방송과 비교해 보니 챔피언 위치와 킬이 거꾸로 보였고 텔레포트·귀환 표시가 부정확했다. 킬이 뒤집힌 원인은 헤더 팀 순서 버그(getLive 의 팀 순서는 시리즈 내내 고정이라 피드의 blueTeamMetadata.esportsTeamId 로 블루 진영을 정해야 한다)였고 별도로 고쳤다. 위치·텔레포트·귀환·밴픽은 피드에 없고 영상으로만 얻을 수 있는데, 36경기 백테스트에서 생존 챔피언 검출이 약 94%에 그쳐 정확하다고 말할 수 없었다. 사용자는 정확한 것만 보여 달라고 요청했다.
- Decision: 방송 영상 분석 전체를 제거한다: 방송 미니맵 패널(m 키)과 영상 동기(v 키), 챔피언 위치 추적(템플릿 매칭, 칼만 필터), 텔레포트·귀환 감지, 메인 뷰 체력바 읽기, HUD 시계 OCR과 킬 플립 동기, kitty·blocks·text 미니맵 그리기(-icons), -src 플래그와 lol minimap 명령, replay 의 VOD 동기, Data Dragon 이미지 내려받기와 캐시, 오프라인 백테스트 하네스, 옵션 메뉴의 미니맵 그리기와 영상 동기 항목, 게임 목록의 VOD 열과 미리보기의 VODs 줄. 밴픽 보기도 정확한 데이터 출처가 없어 만들지 않는다. 보드는 livestats 피드에서 정확히 얻는 값(킬·골드·오브젝트·KDA·CS·체력, 일시정지를 반영한 게임 시계, 오브젝트 타이머, 부활 카운트다운)만 보여 준다. 라이브 부활 시간만 추정이며 ~ 로 표시한다.
- Consequences: yt-dlp, ffmpeg, JS 런타임 의존성이 없어지고 외부 프로세스를 실행하지 않는다. 약 2만 줄의 코드가 사라졌다. 미니맵 패널이 없고 replay 는 피드만 재생한다. 백테스트 하네스(go test -tags backtest, -backtest.out)와 그 수치 문서가 없어져 사망 로직은 deaths_test.go 단위 테스트로만 확인한다. Data Dragon 캐시 디렉터리(UserCacheDir/lolterm/ddragon)와 %LocalAppData%/lolterm/backtest 는 lolterm 이 더 이상 쓰지 않으므로 사용자는 지워도 된다. 챔피언 위치, 텔레포트·귀환, 밴픽은 피드에 들어오기 전에는 보여 주지 않는다. 방송 영상 분석을 다시 도입하려면 정확도 기준(영상 판독이 피드와 어긋나는 비율)을 먼저 정하고 이 결정을 되돌리는 기록을 남긴다. 이전 영상 분석 기록(cautions, adr 의 2026-10 ~ 2026-11-07 기록)은 역사로 남아 있다.
