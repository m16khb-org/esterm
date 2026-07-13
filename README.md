# lole — LoL Esports CLI

League of Legends 프로 대회(MSI, Worlds, LCK, LPL, LEC, LCS 등)의
일정·결과·**실시간 인게임 보드**를 터미널에서 보는 싱글 바이너리 CLI.

lolesports.com 이 쓰는 비공식 엔드포인트를 그대로 사용한다. API 키는 사이트에
하드코딩된 공용 키이므로 별도 발급/설정 불필요.

## 설치

```bash
cd ~/workspace/lole
go build -o lole .
# macOS: Gatekeeper quarantine 해제
xattr -d com.apple.quarantine ./lole 2>/dev/null
# 어디서든 쓰려면
ln -sf "$PWD/lole" ~/.local/bin/lole   # ~/.local/bin 이 PATH 에 있어야 함
```

의존성 없음 (Go 표준 라이브러리만 사용).

## 사용법

```
lole relay     [-l <league>] [-1]          풀스크린 ASCII 실시간 중계 (1초 갱신)
lole live      [-l <league>] [-w]          실시간 경기 + 인게임 보드
lole schedule  [-l <league>] [-n N] [-a]   다가오는 경기 (-a = 최근 결과)
lole leagues                               리그 슬러그/ID 목록
```

플래그
- `-l <league>`  리그 슬러그 필터 (`msi`, `worlds`, `lck`, `lpl`, `lec`, `lcs`, ...)
- `-w`           watch 모드: 30초마다 새로고침
- `-1`           relay: 단일 프레임만 출력 후 종료 (스크린샷/스크립트용)
- `-n N`         표시할 경기 수 (기본 15)
- `-a`           upcoming 대신 최근/종료 경기

## 예시

```bash
lole relay -l msi          # MSI 풀스크린 실시간 중계 (1초 갱신, Ctrl+C 종료)
lole relay -1              # 단일 프레임만 캡처 (미리보기)
lole demo                  # 라이브 게임 없이 중계 화면 미리보기
lole live -l msi -w        # MSI 실시간 보드를 30초 간격으로 갱신
lole schedule -l lck -n 10 # LCK 다음 10경기
lole schedule -a -n 8      # 모든 리그 최근 결과
lole leagues               # 전체 리그 슬러그 확인
```

## 반응형 (responsive)

터미널 **너비와 높이**를 모두 감지해 자동으로 레이아웃을 바꾼다.

- **너비**: 좁아지면 선수 컬럼을 우선순위대로 드롭한다
  (HP숫자 → CS → 챔프 축소 → 이름 축소 → HP바 …). 최소 ~50컬럼까지 동작.
  104컬럼 이상에서는 **양 팀을 좌우 2단**으로 배치해 화면을 넓게 쓴다.
- **높이**: 28줄 이하면 **compact 모드**로 섹션 사이 빈 줄을 없애 줄 수를 줄인다.
  좁고 짧은 터미널에서도 테두리가 깨지지 않게 모든 줄 폭을 정확히 맞춘다.
- 킬/골드 비례 바, 오브젝트 행, 팀명도 가용 폭에 맞춰 자동 적응.

레이아웃 미리보기(임의 크기 지정):
```bash
lole demo -w 120        # 넓은 화면(좌우 2단) 미리보기
lole demo -w 60 -h 20   # 좁고 낮은 터미널 미리보기
```

## 토글로 여는 디테일 (relay 실행 중 키 입력)

`lole relay` 실행 중 누르면 해당 패널이 열리고 다시 누르면 닫힌다
(diff 렌더러가 바뀐 부분만 갱신).

| 키 | 보기 |
|---|---|
| `b` (또는 Tab) | 뷰 **사이클**: stats → items(아이템 풀네임) → runes(룬 페이지 전체) → stats |
| `s` | **골드 차이 스파크라인** (관전 시작 후 누적, 시간별 청/적 리드) |
| `o` | **오브젝트 맵**: 타워 비례바·용 원소+소울·바론/억제/골드 리드 |
| `q` (Ctrl+C) | 종료 |

> 드래곤 4번째 팀은 기본 보드의 `DRG` 옆에 `★SOUL` 로 표시된다.
> 아이템/룬 이름은 Data Dragon(`ddragon.leagueoflegends.com`)에서 패치 버전에 맞춰
> 런타임 페치+캐시한다(실패 시 `#id` 폴백).

## relay (실시간 중계)

`lole relay` 는 터미널 전체를 쓰는 풀스크린 중계 화면이다.

- **1초마다 화면 재생성**: 게임 클럭(스폰 이후 경과)과 LIVE 펄스가 매 초 갱신되고,
  인게임 데이터는 Riot 피드가 새 프레임을 줄 때마다(약 10초 단위) 반영된다.
- 킬/골드는 비례 바(청팀 cyan / 적팀 red), 오브젝트(DRG/BRN/TWR/INB) 카운트,
  선수별 KDA·CS·골드·**HP바(체력에 따라 녹/황/적색)** 표시.
- alt screen + 커서 숨김 사용. `Ctrl+C` 로 종료하면 터미널이 원래대로 복구된다.
- 게임 id 는 폴링 3회마다(약 30초) 재탐색해 세트 전환(다음 게임 시작)을 자동으로 따라간다.
- 터미널 너비를 자동 감지해 70~120 컬럼에 맞춘다.

> 참고: Riot 중계 feed는 게임이 실제로 중계(spectate)되기 시작해야 프레임이 밀려온다.
> 스케줄이 inProgress 라도 방송 시작 전엔 데이터가 비어오며, 그땐 `waiting for in-game data…`
> 로 표시된다. 이건 정상 동작이다.

## 데이터 소스

| 엔드포인트 | 용도 |
|---|---|
| `esports-api.lolesports.com/persisted/gw/getLive` | 진행중 경기 + gameId |
| `.../getSchedule`, `/getLeagues` | 일정·결과·리그 |
| `feed.lolesports.com/livestats/v1/window/{gameId}?startingTime=` | 팀 총합(킬/골드/용/바론/타워/억제기) + 선수 KDA/CS/체력 |
| `feed.lolesports.com/livestats/v1/details/{gameId}?startingTime=` | 선수별 아이템/데미지/시야 (상세) |

`window` 는 `startingTime`(10초 단위 정렬) 파라미터가 있어야 최신 스냅샷이 온다.
없으면 게임 스폰 시점 프레임만 리턴한다.

모든 시간은 KST 로 표시.
