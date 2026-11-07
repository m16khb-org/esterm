<div align="center">

# lolterm

**리그 오브 레전드 e스포츠를 터미널에서 보는 CLI**

라이브 보드와 공식 경기 다시 보기. livestats 피드가 주는 정확한 값만 보여 줍니다

<p>
  <a href="https://github.com/m16khb-org/lolterm/releases/tag/v0.3.0"><img alt="release" src="https://img.shields.io/github/v/release/m16khb-org/lolterm?style=flat-square&label=release&color=4de1ff"></a>
  <a href="go.mod"><img alt="go 1.21+" src="https://img.shields.io/badge/go-1.21%2B-00ADD8?style=flat-square&logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="license" src="https://img.shields.io/badge/license-GPL--3.0--or--later-blue?style=flat-square"></a>
  <a href="https://github.com/m16khb-org/lolterm/stargazers"><img alt="stars" src="https://img.shields.io/github/stars/m16khb-org/lolterm?style=flat-square&color=4de1ff"></a>
</p>

[한국어](README.md) · [English](https://m16khb-org.github.io/lolterm/?lang=en) · [中文](https://m16khb-org.github.io/lolterm/?lang=zh) · [웹 소개](https://m16khb-org.github.io/lolterm/) · [설치](INSTALL.md)

<sub>macOS · Linux · Windows · Go 1.21+</sub>


</div>

> **v0.3.0** — 공개 릴리스입니다. 창 크기마다 레이아웃을 다듬었고, 네트워크가 끊기거나 일시정지가 있는 경기에서도 보드가 안정적으로 이어집니다. Windows 네이티브 지원도 들어 있습니다. [릴리스 노트](https://github.com/m16khb-org/lolterm/releases/tag/v0.3.0)에서 변경 사항과 설치 방법을 확인하세요.

[왜 lolterm](#왜-lolterm인가) · [한눈에](#한눈에) · [설치](#설치) · [빠른 시작](#빠른-시작) ·
[대화형 앱](#대화형-앱) · [보드](#보드) · [작동 방식](#작동-방식) · [검증](#검증) ·
[문서](#문서) · [기여](#기여) · [라이선스](#라이선스)

## 왜 lolterm인가

Riot의 livestats 피드는 점수, 오브젝트, 선수 스탯을 정확하게 알려 줍니다. lolterm은 그 값만 터미널에 그대로 옮깁니다. 영상에서 추정한 값은 쓰지 않습니다.

- **피드의 값만 씁니다.** 킬·골드·오브젝트·KDA·CS·체력은 피드 그대로이고, 게임 시계는 일시정지를 반영해 보정합니다. 추정값은 `~`로 표시합니다(라이브 부활 시간).
- **터미널에서 끝납니다.** 라이브 보드와 다시 보기를 한 화면에서 보고, 클릭·더블클릭·휠로 조작합니다. UI는 한국어·영어·중국어를 지원합니다.
- **설치가 가볍습니다.** Go 표준 라이브러리만 쓰는 단일 바이너리이고 외부 도구가 필요 없습니다.

## 한눈에

| 기능 | 내용 |
|---|---|
| **라이브 보드** | 킬·골드 비례 바, 용·바론·타워·억제기 수, 선수별 KDA·CS·골드·HP 바를 0.5초마다 갱신 |
| **다시 보기** | 끝난 세트를 livestats 피드로 되감아 재생. `Space`·`←`·`→`·배속 지원 |
| **오브젝트 타이머** | 용·바론이 나오기까지 남은 시간 (피드의 처치 시각과 패치 규칙으로 계산) |
| **부활 카운트다운** | 사망한 선수의 `☠ ~Ns`. 라이브는 추정(`~`), 다시 보기는 피드에서 읽은 정확한 시각 |
| **마우스 조작** | 클릭으로 창 선택과 항목 이동, 더블클릭으로 열기, 휠로 목록 스크롤 |
| **3개 언어** | 한국어·영어·중국어 UI. `-lang ko\|en\|zh` 또는 `LOLTERM_LANG`, 옵션 메뉴에서 전환 |
| **단일 바이너리** | Go 표준 라이브러리만 사용. 외부 의존성 없음 |

## 설치

Go 1.21 이상이 필요합니다. 다른 도구는 필요 없습니다.

```bash
go install github.com/m16khb-org/lolterm@latest
# $(go env GOPATH)/bin 이 PATH 에 있어야 합니다
```

Windows 10 1809 이상에서도 네이티브로 빌드하고 실행합니다. 저장소에서 `go install .` 한 뒤 `lolterm`을 실행합니다.
자세한 사전 점검·PATH·검증 절차는 **[INSTALL.md](INSTALL.md)** 에 있습니다.

<details>
<summary>소스에서 빌드하기</summary>

```bash
git clone https://github.com/m16khb-org/lolterm.git
cd lolterm
mkdir -p ~/.local/bin
go build -o ~/.local/bin/lolterm .
```

</details>

## 빠른 시작

```bash
lolterm                              # 대화형 앱
lolterm -lang ko                     # 한국어 UI (en, zh)
lolterm lol replay                   # 최근 종료 경기와 -match 에 쓸 id 목록
lolterm lol replay -match 115660588583040710 -g 1
lolterm lol relay -l msi             # MSI 풀스크린 실시간 중계
lolterm lol demo                     # 라이브 경기 없이 보드 한 프레임 미리보기
lolterm -version                     # 설치된 버전 (버그 제보에 첨부)
```

대화형 앱에서 리그를 고르고 `Enter`로 경기를 열면 라이브 보드나 다시 보기가 시작됩니다.

## 대화형 앱

인자 없이 `lolterm`을 실행하면 키보드와 마우스로 쓰는 앱이 열립니다. 왼쪽에 `[1] Leagues`, `[2] Matches`, `[3] Games` 목록이 쌓이고,
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
| `,` | 옵션 메뉴 (폴링 간격, 왼쪽 열 표시, 언어) |
| `?` | 현재 문맥의 키 도움말 (화면보다 길면 `↑` `↓`로 스크롤) |
| `q` / `Ctrl+C` | 종료 |

언어는 `lolterm -lang ko`, `lolterm -lang zh`처럼 실행하거나 `LOLTERM_LANG=ko`를 설정합니다.
기본값은 영어이고, 옵션 메뉴의 `Language` 항목에서도 바꿀 수 있습니다.
화면은 최소 40×12, 보드는 50열 이상이 필요합니다. 대화형 화면은 표준 입출력이 터미널일 때만 열리고,
파이프나 리디렉션으로 실행하면 오류로 끝납니다. 한 프레임만 필요하면 `lolterm lol relay -1`을 씁니다.

보드에 포커스가 있을 때는 `Space` 일시정지, `←` `→` ±10초, `+` `-` 배속(1×/2×/4×, 다시 보기), `b` stats→items→runes,
`s` 골드 스파크라인, `o` 오브젝트 맵, `f` 확대를 씁니다.
단독 실행한 `relay`·`replay` 보드에서는 `Ctrl+L`로 화면 전체를 다시 그립니다.

## 보드

보드의 모든 값은 livestats 피드에서 옵니다.

| 줄 | 내용 |
|---|---|
| 헤더 | 리그, 블록, 게임 시계, 패치. 게임 시계는 일시정지 구간을 뺀 값이며, 보드를 연 뒤 일어난 일시정지뿐 아니라 연 시점 이전의 일시정지도 피드 기록에서 찾아 반영합니다 |
| 팀 | 블루 팀이 왼쪽입니다. 팀 순서는 피드의 `blueTeamMetadata`가 정합니다 |
| 킬·골드 | 킬 수와 골드 비례 바 |
| 오브젝트 | 용·바론·타워·억제기 수. 영혼 획득은 `★SOUL` |
| 타이머 | 용·바론이 나오기까지 남은 시간(`UP`은 지금 구덩이에 있음) |
| 선수 | K/D/A, CS, 골드, HP 바, 사망 시 `☠ ~Ns` 부활 카운트다운 |

부활 카운트다운은 라이브에서 레벨과 게임 시간으로 계산한 추정이라 `~`가 붙고, 다시 보기에서는 피드에서 읽은 정확한 시각입니다.
용·바론 타이머는 피드가 알려 준 처치 시각에 패치 규칙의 재등장 시간을 더해 계산합니다.
`b`는 stats → items → runes 보기를, `s`는 골드 차이 스파크라인을, `o`는 오브젝트 맵을 켜고 끕니다.

### 하지 않는 것

챔피언 위치, 텔레포트·귀환, 밴픽은 공식 피드에 들어 있지 않으므로 lolterm은 보여 주지 않습니다. 방송 영상을 읽어 추정하지도 않습니다.

## 작동 방식

1. **피드** — `getLive`·`getSchedule`·`getEventDetails`로 경기와 세트를 찾고, livestats 피드(`window`·`details`)에서 점수판·게임 시계·선수 스탯을 가져옵니다.
2. **재생** — 피드는 10초 분량씩 백그라운드에서 받아 두고, 화면은 네트워크를 건드리지 않고 0.5초마다 쌓인 프레임을 재생합니다. 지연은 최근 수신 지연에 맞춰 자동 조절됩니다.
3. **게임 시계** — 피드의 일시정지 구간을 빼서 계산합니다. 경기 중간에 보드를 열어도 그 전의 일시정지를 피드 기록에서 찾아 반영합니다.
4. **다시 보기** — 끝난 세트의 프레임을 10초 창 단위로 받아 저장하고, 재생 위치에 맞는 프레임을 보여 줍니다. 같은 창은 한 번만 받습니다.

```text
lolesports API ──▶ livestats feed ──▶ playout (0.5s) ──▶ board
                                      ├─ game clock (pauses removed)
                                      ├─ deaths · respawn
                                      └─ dragon / baron timers
```

## 검증

`go test ./...`가 확인하는 것은 피드 파싱과 일시정지 처리, 사망·부활 계산, 오브젝트 타이머, 다시 보기 저장소, 키·마우스·폭 계산, UI 번역입니다.
단위 테스트이며, 화면은 `lolterm lol demo`로 합성 프레임을 그려 볼 수 있습니다.

## 문서

| 문서 | 용도 |
|---|---|
| [INSTALL.md](INSTALL.md) | 사전 점검, PATH, 검증 체크리스트 |
| [구현 상세](docs/internals.md) | 피드, 재생 지연, 일시정지, 사망·부활, 오브젝트 타이머, 다시 보기, 데이터 소스 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 개발 환경, 테스트, 커밋·PR 규칙 |
| [.issueops/ARCHITECTURE.md](.issueops/ARCHITECTURE.md) | 구조와 설계 결정 |
| [.issueops/TESTING.md](.issueops/TESTING.md) | 검증 기준 |
| [웹 소개](https://m16khb-org.github.io/lolterm/) | 소개와 설치 안내 (한국어·영어·중국어) |

## 기여

이슈와 PR을 환영합니다. 버그를 재현했다면 경기 ID·세트 번호·터미널·재현 시각을 함께 적어 주세요.

```bash
go test -count=1 ./...          # 전체 테스트
go build -o ~/.local/bin/lolterm .
```

UI를 바꿨다면 스크린샷을 PR에 첨부합니다. 커밋은 Conventional Commits 형식을 따르고, 비밀값·로컬 캐시·생성물은 올리지 않습니다.
자세한 내용은 **[CONTRIBUTING.md](CONTRIBUTING.md)** 를 참고하세요. 마음에 들었다면 GitHub에서 스타를 눌러 주세요.

## 라이선스

GPL-3.0-or-later. 전문은 [LICENSE](LICENSE)를 참고합니다.

Copyright (C) 2026 m16khb

> lolesports.com의 비공식 엔드포인트를 사용합니다. Riot Games와 공식적인 관련이 없습니다.
