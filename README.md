<div align="center">

# esterm

**리그 오브 레전드 e스포츠를 터미널에서 보는 CLI**

라이브 보드, 공식 VOD 다시 보기, 방송 미니맵에서 직접 읽는 챔피언 위치

<p>
  <a href="https://github.com/m16khb-org/esterm/releases/tag/v0.1.0"><img alt="release" src="https://img.shields.io/github/v/release/m16khb-org/esterm?style=flat-square&label=release&color=4de1ff"></a>
  <a href="go.mod"><img alt="go 1.21+" src="https://img.shields.io/badge/go-1.21%2B-00ADD8?style=flat-square&logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="license" src="https://img.shields.io/badge/license-GPL--3.0--or--later-blue?style=flat-square"></a>
  <a href="https://github.com/m16khb-org/esterm/stargazers"><img alt="stars" src="https://img.shields.io/github/stars/m16khb-org/esterm?style=flat-square&color=4de1ff"></a>
</p>

[한국어](README.md) · [English](https://m16khb-org.github.io/esterm/?lang=en) · [中文](https://m16khb-org.github.io/esterm/?lang=zh) · [웹 소개](https://m16khb-org.github.io/esterm/) · [설치](INSTALL.md)

<sub>macOS · Linux · Go 1.21+</sub>

<img src="docs/assets/hero.png" alt="esterm 미니맵: 팀 링 바깥의 귀환 R, 텔레포트 T, 레드 팀 채널링 ? 아우라" width="100%" />

</div>

> **v0.1.0** — 첫 공개 릴리스입니다. [릴리스 노트](https://github.com/m16khb-org/esterm/releases/tag/v0.1.0)에서 변경 사항과 설치 방법을 확인하세요.

[왜 esterm](#왜-esterm인가) · [한눈에](#한눈에) · [설치](#설치) · [빠른 시작](#빠른-시작) ·
[대화형 앱](#대화형-앱) · [미니맵](#미니맵) · [작동 방식](#작동-방식) · [검증](#검증) ·
[터미널 설정](#터미널-설정) · [문서](#문서) · [기여](#기여) · [라이선스](#라이선스)

## 왜 esterm인가

Riot의 livestats 피드는 점수와 오브젝트는 알려 주지만 챔피언 좌표는 주지 않습니다. esterm은 공식 방송 화면에서 미니맵을 직접 읽어 이 빈칸을 채웁니다.

- **방송을 읽습니다.** 공식 1080p 방송의 미니맵과 게임 시계를 인식하고, 초상화 템플릿 매칭·칼만 필터·메인 뷰 체력바를 함께 사용해 10명의 위치를 추정합니다.
- **터미널에서 끝납니다.** 라이브 보드와 다시 보기를 한 화면에서 보고, 클릭·더블클릭·휠로 조작합니다. UI는 한국어·영어·중국어를 지원합니다.
- **숫자로 확인합니다.** Worlds·LCK·LPL·LEC 16경기 백테스트에서 생존 챔피언 검출 94.0%, 죽은 챔피언 오검출 0.00%를 기록했습니다.

<p align="center">
  <a href="docs/assets/demo.mp4"><img src="docs/assets/demo.gif" alt="esterm 실제 구동 화면: Worlds 리플레이 보드와 실시간 미니맵" width="100%" /></a>
</p>
<p align="center"><sub>Worlds 리플레이 보드 · 15초 실제 구동 영상 · <a href="docs/assets/demo.mp4">MP4</a></sub></p>

## 한눈에

| 기능 | 내용 |
|---|---|
| **라이브 보드** | 킬·골드 비례 바, 오브젝트 수, 선수별 KDA·CS·골드·HP 바를 0.5초마다 갱신 |
| **다시 보기** | 공식 VOD 영상과 livestats 피드를 같은 게임 시각에 맞춰 재생. `Space`·`←`·`→`·배속 지원 |
| **방송 미니맵** | 공식 1080p 방송 4종(Worlds·LCK·LPL·LEC) 레이아웃 자동 인식, 초당 5프레임 추적 |
| **상태 표시** | 사망 `✕`+부활 초, 놓침은 흐린 초상화+`?`, 귀환 `R`·텔레포트 `T` |
| **마우스 조작** | 클릭으로 창 선택과 항목 이동, 더블클릭으로 열기, 휠로 목록 스크롤 |
| **3개 언어** | 한국어·영어·중국어 UI. `-lang ko\|en\|zh` 또는 `ESTERM_LANG`, 옵션 메뉴에서 전환 |
| **단일 바이너리** | Go 표준 라이브러리만 사용. 외부 의존성은 미니맵용 `yt-dlp`·`ffmpeg`·JS 런타임뿐 |

## 설치

Go 1.21 이상이 필요합니다. 미니맵을 쓰려면 `yt-dlp`, `ffmpeg`, JS 런타임(`deno` 권장, 없으면 `bun` 또는 `node`)이 필요합니다.

```bash
go install github.com/m16khb-org/esterm@latest
# $(go env GOPATH)/bin 이 PATH 에 있어야 합니다
```

미니맵 의존성은 macOS에서 `brew install yt-dlp ffmpeg deno`, Linux에서 `apt install ffmpeg`와 `pipx install yt-dlp`로 설치합니다.
자세한 사전 점검·PATH·검증 절차는 **[INSTALL.md](INSTALL.md)** 에 있습니다.

<details>
<summary>소스에서 빌드하기</summary>

```bash
git clone https://github.com/m16khb-org/esterm.git
cd esterm
mkdir -p ~/.local/bin
go build -o ~/.local/bin/esterm .
```

</details>

## 빠른 시작

```bash
esterm                              # 대화형 앱
esterm -lang ko                     # 한국어 UI (en, zh)
esterm lol replay                   # 최근 종료 경기와 -match 에 쓸 id 목록
esterm lol replay -match 115660588583040710 -g 1
esterm lol relay -l msi             # MSI 풀스크린 실시간 중계
```

대화형 앱에서 리그를 고르고 `Enter`로 경기를 열면 라이브 보드나 다시 보기가 시작됩니다.
방송 미니맵은 공식 1080p 방송에서만 동작하며, 리플레이·데스크·밴픽 화면에서는 챔피언을 숨기고 이유를 표시합니다.

## 대화형 앱

인자 없이 `esterm`을 실행하면 키보드와 마우스로 쓰는 앱이 열립니다. 왼쪽에 `[1] Leagues`, `[2] Matches`, `[3] Games` 목록이 쌓이고,
오른쪽 `[0]` 창은 선택한 경기의 미리보기나 실행 중인 보드를 보여 줍니다.

| 입력 | 동작 |
|---|---|
| `j` `k` / `↑` `↓` | 목록 이동 |
| `h` `l` / `←` `→` | 이전·다음 창 |
| `Tab` / `1` `2` `3` `0` | 창 포커스 이동 |
| `[` `]` | Matches 탭 전환 (Live / Upcoming / Recent) |
| `Enter` | 열기 (리그 → 경기 → 세트 → 보드) |
| 클릭 | 창 포커스와 행 선택 |
| 더블클릭 | 경기·세트 열기, 보드 확대 전환 |
| 휠 | 포인터 아래 목록 스크롤 |
| `,` | 옵션 메뉴 (미니맵 그리기, 영상 동기화, 폴링 간격, 언어) |
| `?` | 현재 문맥의 키 도움말 |
| `q` / `Ctrl+C` | 종료 |

언어는 `esterm -lang ko`, `esterm -lang zh`처럼 실행하거나 `ESTERM_LANG=ko`를 설정합니다.
기본값은 영어이고, 옵션 메뉴의 `Language` 항목에서도 바꿀 수 있습니다.

보드에 포커스가 있을 때는 `Space` 일시정지, `←` `→` ±10초, `+` `-` 배속, `b` stats→items→runes,
`s` 골드 스파크라인, `o` 오브젝트 맵, `m` 미니맵 패널, `v` 방송 영상 동기화, `f` 확대를 씁니다.

## 미니맵

`m` 토글 또는 `esterm lol minimap`은 방송 영상의 미니맵에서 챔피언 위치를 직접 검출합니다.
검출은 순수 Go로 합니다: Data Dragon/CommunityDragon 초상화 템플릿, 헝가리안 배정, 칼만 필터, 메인 뷰 체력바 융합.

<p align="center">
  <img src="docs/assets/states.png" alt="라이브, 사망, 놓침, 혼합 상태의 미니맵" width="100%" />
</p>

| 상태 | 표시 |
|---|---|
| 라이브 | 초상화 + 팀 색 링 + 라인 배지 `T` `J` `M` `B` `S` |
| 사망 | 회색 X 아이콘 + 아이콘에 붙은 부활 초. 피드의 사망 수로 판단합니다 |
| 놓침 | 흐린 초상화 + 회색 `?`. 마지막 측정 위치를 추정해 유지합니다 |
| 귀환 | 팀 링 바깥 팀 색 아우라 + `R`. 블루 팀은 청록, 레드 팀은 빨강입니다 |
| 텔레포트 | 팀 링 바깥 보라 아우라 + `T` |
| 레드 팀 채널링 | 팀 링 바깥 빨강 아우라 + `?`. 5초를 넘기면 귀환 `R`로 바뀝니다 |

<p align="center">
  <img src="docs/assets/channels.png" alt="팀 링 바깥 아우라로 귀환 R, 텔레포트 T, 레드 팀 채널링 ?를 표시한 미니맵" width="100%" />
</p>

채널링은 아이콘 바깥 링의 **색**을 읽어 판정합니다. 팀 색 링은 그대로 두고 그 바깥에 물결 모양 아우라를 그립니다.
밝은 링이 2프레임 연속 잡혀야 시작하고, 링이 흐려져도 챔피언이 그 자리에 있는 동안은 유지합니다.
방송은 블루 팀 귀환을 청록, 블루 팀 텔레포트를 초록으로 그리지만 레드 팀은 둘 다 빨강입니다.
그래서 레드 팀은 시간으로 구분합니다. 텔레포트는 4초, 귀환은 8초 동안 채널링하므로 빨간 링이 5초를 넘기면 귀환입니다.
그보다 짧은 빨간 링(텔레포트, 취소한 귀환, 바론 버프의 4초 귀환)은 `?`로 남습니다.

```bash
esterm lol minimap -src https://www.youtube.com/watch?v=UWBRuboiCXk \
  -ss 1500 -game 115660588583040711 -n 20 -json
```

`-json`은 프레임마다 한 줄을 출력합니다. `present`(레이아웃 잠금), `clock`, `kills`, `visible`,
`champions[{pid,champ,role,team,visible,x,y,score,chan}]`을 담습니다. 좌표는 게임 단위입니다.

## 작동 방식

1. **피드** — `getEventDetails`로 경기·세트·VOD를 찾고, livestats 피드에서 점수판·게임 시계·선수 스탯을 가져옵니다.
2. **방송** — 공식 VOD에서 1080p 스트림을 열고, 상단 게임 시계 글꼴로 방송 레이아웃을 판별합니다.
3. **추적** — 미니맵 영역을 잘라 초상화를 템플릿 매칭하고, 헝가리안 배정과 칼만 필터로 프레임 사이의 이동을 잇습니다. 메인 뷰 체력바와 피드의 사망·부활 정보를 함께 씁니다.
4. **정렬** — 방송 시계와 피드의 킬 이벤트를 대조해 오프셋을 계속 보정합니다. 점수판·선수 스탯·오브젝트·미니맵이 같은 게임 시각을 가리킵니다.

```text
lolesports API ──▶ livestats feed ──▶ scoreboard · clock · deaths
                                      │
official VOD ──▶ ffmpeg ──▶ minimap ──┴──▶ template match ──▶ Kalman ──▶ terminal
                              + game clock        (Go, pure)
```

## 검증

완료된 공식 경기 16경기(Worlds 7, LCK 3, LPL 3, LEC 3)를 방송 영상 전체로 돌리는 백테스트가 있습니다.
앱과 같은 `mmPipeline`·`mmSync`·사망 로직을 사용하고, livestats 피드를 정답으로 삼습니다.

```bash
go test -tags backtest -run TestBacktest -timeout 3h -v . -backtest.out=/tmp/backtest.json
```

| 리그 | 경기 | 생존 검출 | 죽은 챔피언 오검출 | 킬 플립 ≤1초 | 5초↑ 공백(건/초) |
|---|---|---|---|---|---|
| worlds | 7 | 약 94.4% | 0.00% | 98.0% | 532/11498 |
| lck | 3 | 94.2% | 0.00% | 97.5% | 299/6536 |
| lpl | 3 | 91.2% | 0.00% | 91.7% | 256/5364 |
| lec | 3 | 95.8% | 0.00% | 100% | 249/4708 |
| **전체** | **16** | **약 94.0%** | **0.00%** | **97.2%** | **1336/28106** |

- 16경기 중 13경기가 기준을 통과했습니다.
- 부활 추정 중앙값 0.24초·p90 0.45초, 시계 판독 94.8~98.9%, 틀린 재잠금 0회.
- 프로세스 CPU는 프레임당 21~28ms(5fps, 메인 뷰 포함)로 영상 시간의 7.5~9.2배 속도입니다.

자세한 측정 기준과 경기별 수치는 **[구현 상세](docs/internals.md)** 에 있습니다.

## 터미널 설정

`-icons auto|kitty|blocks|text` (기본 `auto` = kitty → blocks → text).

| 모드 | 내용 |
|---|---|
| `kitty` | 픽셀 지도 + 챔피언 초상화. kitty, Ghostty, WezTerm, herdr에서 동작 |
| `blocks` | 트루컬러 반블록 지도 + 라인 글자. 작은 화면에 적합 |
| `text` | 점자 지도 + 라인 글자. 트루컬러가 없을 때의 마지막 대안 |

`auto`는 `TERM_PROGRAM`(ghostty/wezterm/herdr), `KITTY_WINDOW_ID`, `TERM=xterm-kitty`를 보고 kitty를 고릅니다.
환경변수로 판단이 안 되면 시작할 때 kitty 그래픽 질의를 보내 최대 0.2초 기다립니다.

## 문서

| 문서 | 용도 |
|---|---|
| [INSTALL.md](INSTALL.md) | 사전 점검, PATH, 미니맵 의존성, 검증 체크리스트 |
| [구현 상세](docs/internals.md) | 미니맵 파이프라인, 동기화, 백테스트 전체 수치, 데이터 소스 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 개발 환경, 테스트, 커밋·PR 규칙 |
| [.issueops/ARCHITECTURE.md](.issueops/ARCHITECTURE.md) | 구조와 설계 결정 |
| [.issueops/TESTING.md](.issueops/TESTING.md) | 검증 기준 |
| [웹 소개](https://m16khb-org.github.io/esterm/) | 스크린샷과 설치 안내 (한국어·영어·중국어) |

## 기여

이슈와 PR을 환영합니다. 버그를 재현했다면 경기 ID·세트 번호·터미널·`-icons` 모드·재현 시각을 함께 적어 주세요.

```bash
go test -count=1 ./...          # 전체 테스트
go build -o ~/.local/bin/esterm .
```

UI를 바꿨다면 스크린샷을 PR에 첨부합니다. 커밋은 Conventional Commits 형식을 따르고, 비밀값·로컬 캐시·생성물은 올리지 않습니다.
자세한 내용은 **[CONTRIBUTING.md](CONTRIBUTING.md)** 를 참고하세요. 마음에 들었다면 GitHub에서 스타를 눌러 주세요.

## 라이선스

GPL-3.0-or-later. 전문은 [LICENSE](LICENSE)를 참고합니다.

Copyright (C) 2026 m16khb

> lolesports.com의 비공식 엔드포인트를 사용합니다. 영상은 **개인 시청 목적**으로만 처리하며,
> 녹화·저장·재배포하지 마세요. 각 플랫폼의 약관을 따릅니다. Riot Games와 공식적인 관련이 없습니다.
