# esterm 설치 가이드 (에이전트용)

이 문서는 Claude Code, Codex, omp 같은 코딩 에이전트가 **사용자에게 되묻지 않고** 처음부터 끝까지 따라 할 수 있게 쓰였다.
모든 단계는 멱등(여러 번 실행해도 안전)이며, 각 단계에 확인 명령과 기대 출력이 있다.

## 에이전트용 요약

아래 순서로 실행하고, 분기는 "→" 를 따른다.

```bash
# 1. 사전 점검
go version                      # → go1.21 이상이 아니면: Go 설치(https://go.dev/dl/ 또는 macOS: brew install go) 후 재시도
git --version                   # → 없으면 설치 (소스 빌드를 쓸 때만 필요)

# 2. 설치 (둘 중 하나)
go install github.com/m16khb-org/esterm@latest      # 바이너리: $(go env GOPATH)/bin/esterm (원격에 푸시된 버전)
#   소스 체크아웃이 있으면 이쪽을 우선 (로컬 최신 기능 포함):  cd <repo> && mkdir -p ~/.local/bin && go build -o ~/.local/bin/esterm .

# 3. PATH (없을 때만 추가)
BIN="$(go env GOPATH)/bin"          # 소스 빌드를 썼다면 BIN="$HOME/.local/bin"
case ":$PATH:" in *":$BIN:"*) ;; *) echo "export PATH=\"$BIN:\$PATH\"" >> ~/.zshrc; export PATH="$BIN:$PATH";; esac
#   bash 사용자는 ~/.zshrc 대신 ~/.bashrc

# 4. 기본 검증 (미니맵 의존성 없이도 동작)
command -v esterm && esterm lol demo -w 100 -h 40 | head -3
#   인자 없이 `esterm` 을 실행하면 대화형 앱이 열린다 (q 로 종료). 에이전트의 비대화형 셸에서는 열지 말 것.

# 5. 미니맵(선택): 하나라도 없을 때만 설치
command -v ffmpeg  >/dev/null || echo "ffmpeg 필요"
command -v yt-dlp  >/dev/null || echo "yt-dlp 필요"
command -v deno >/dev/null || command -v bun >/dev/null || command -v node >/dev/null || echo "JS 런타임 필요"
#   macOS: brew install yt-dlp ffmpeg deno      Linux: 4장 참고

# 6. 최종 검증: 6장의 체크리스트
```

사용자 개입이 필요한 경우는 없다. 라이브 경기가 없을 때 `esterm lol relay -1` 이 `NO LIVE GAMES RIGHT NOW` 를 출력하는 것은 정상이다.

## 1. 목적과 지원 OS

`esterm` 은 라이브 LoL e스포츠 보드를 터미널에 그리는 Go 단일 바이너리다 (표준 라이브러리만 사용, 외부 모듈 없음).

| OS | 상태 |
|---|---|
| macOS arm64 / amd64 | 기본 지원. ffmpeg 하드웨어 디코드(VideoToolbox) 자동 사용 |
| Linux | 빌드와 실행 가능(`GOOS=linux go build` 확인). 보드와 미니맵 영상 처리는 ffmpeg/yt-dlp 가 있으면 동작 |
| Windows | 확인하지 않음 |

raw 모드(키 입력)는 darwin 과 linux 모두 지원한다 (`term_darwin.go` 는 `TIOCGETA/TIOCSETA`, `term_linux.go` 는 `TCGETS/TCSETS`).
대화형 앱, `relay`, `replay` 의 키 입력(`b` `s` `o` `m` `v` `q`)은 두 OS 에서 모두 즉시 반응한다. `GOOS=linux go build` 로 빌드되는 것만 확인했고, Linux 터미널에서 직접 실행해 보지는 않았다.

## 2. 사전 점검

```bash
go version
# 기대: go version go1.21 이상 (go.mod 의 `go 1.21`). 예: go version go1.27.2 darwin/arm64
git --version            # 소스 빌드에만 필요
```

네트워크 (3개 호스트). 루트 경로는 403 을 돌려주므로 실제 경로로 확인한다:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -H "x-api-key: 0TvQnueqKa5mxJntVWt0w4LpLfEkrV1Ta8rQBb9Z" "https://esports-api.lolesports.com/persisted/gw/getLeagues?hl=en-US"   # 기대: 200
curl -s -o /dev/null -w "%{http_code}\n" "https://feed.lolesports.com/livestats/v1/window/115660588583040711"                                                  # 기대: 200
curl -s -o /dev/null -w "%{http_code}\n" "https://ddragon.leagueoflegends.com/api/versions.json"                                                              # 기대: 200
```

(`curl -sI https://<host>/` 는 403 이 나온다. 정상이며 연결 가능하다는 뜻이다. API 키는 lolesports.com 이 쓰는 공용 키이며 별도 발급은 필요 없다.)

PATH 점검:

```bash
command -v esterm || echo "esterm 이 PATH 에 없음 → 3장 PATH 설정"
```

## 3. 설치

### (a) go install

```bash
go install github.com/m16khb-org/esterm@latest
# 설치 위치: $(go env GOBIN) 가 설정돼 있으면 그곳, 아니면 $(go env GOPATH)/bin
# 주의: @latest 는 원격 저장소에 올라간 버전이다. 로컬 체크아웃에만 있는 변경(미니맵·동기화 등)이 필요하면 (b) 소스 빌드를 쓴다.
```

### (b) 소스 빌드

```bash
git clone git@github.com:m16khb-org/esterm.git && cd esterm     # 또는 https://github.com/m16khb-org/esterm.git
mkdir -p ~/.local/bin
go build -o ~/.local/bin/esterm .
```

### PATH 추가 (없을 때만)

```bash
BIN="$(go env GOPATH)/bin"      # (b) 로 설치했다면 BIN="$HOME/.local/bin"
case ":$PATH:" in
  *":$BIN:"*) echo "이미 PATH 에 있음" ;;
  *) echo "export PATH=\"$BIN:\$PATH\"" >> ~/.zshrc    # bash 는 ~/.bashrc
     export PATH="$BIN:$PATH" ;;
esac
```

`~/.zshrc` 에 이미 같은 줄이 있는데 현재 셸에만 반영되지 않았다면 `source ~/.zshrc` 를 쓴다(중복 추가 방지: `grep -q "$BIN" ~/.zshrc || ...`).

### macOS quarantine

브라우저로 내려받은 바이너리를 실행할 때 Gatekeeper 가 막는 경우에만 해당한다. `go install`/`go build` 로 만든 바이너리에는 필요 없다.

```bash
xattr -d com.apple.quarantine "$(command -v esterm)" 2>/dev/null || true
```

## 4. 미니맵 선택 의존성

대화형 앱·`esterm lol relay`·`esterm lol replay` 의 `m` 토글과 `esterm lol minimap` 에만 필요하다. 없어도 나머지 기능은 모두 동작한다.

| 도구 | 용도 | 비고 |
|---|---|---|
| `ffmpeg` | 영상 디코드·크롭 | |
| `yt-dlp` | YouTube/Twitch 페이지에서 미디어 URL 해석 | `-src` 가 로컬 파일이나 미디어 URL 이면 불필요 |
| JS 런타임 | yt-dlp 의 YouTube 해석 | `deno` 권장. 없으면 `bun`, `node` 순으로 `--js-runtimes` 를 자동으로 붙인다 |

macOS:

```bash
brew install yt-dlp ffmpeg deno
```

Linux (Debian/Ubuntu 예):

```bash
sudo apt update && sudo apt install -y ffmpeg pipx
pipx install yt-dlp          # 또는 python3 -m pip install --user yt-dlp
# JS 런타임 중 하나: curl -fsSL https://deno.land/install.sh | sh   또는   sudo apt install -y nodejs
```

PATH 밖의 실행 파일은 환경변수로 지정한다 (`minimap_video.go` 의 `mmTool`):

```bash
export ESTERM_FFMPEG=/path/to/ffmpeg
export ESTERM_YTDLP=/path/to/yt-dlp
```

누락 보고 방식: 미니맵 패널의 상태줄에 `missing tools: <목록> — brew install yt-dlp ffmpeg deno` 로 표시된다. 목록에는 없는 실행 파일 이름(오버라이드한 경우 그 경로)이,
JS 런타임이 하나도 없으면 `deno|bun|node` 가 들어간다. 프로그램은 종료되지 않고 보드는 계속 그려진다.
실행 예 (`ESTERM_FFMPEG=/nonexist esterm lol minimap -src https://www.youtube.com/watch?v=UWBRuboiCXk -game 115660588583040711 -n 1`):

```
missing tools: /nonexist — brew install yt-dlp ffmpeg deno
```

## 5. 터미널 설정

`-icons auto|kitty|blocks|text` (relay/minimap/replay/demo, 기본 `auto`). 대화형 앱에서는 옵션 메뉴(`,`)의 Minimap drawing 이 같은 역할을 한다. `auto` 는 kitty → blocks → text 순으로 내려간다.

- `kitty` (픽셀 이미지): 다음 중 하나면 지원으로 본다 (`minimap_kitty.go` `kittyEnvSupported`).
  - `TERM_PROGRAM` 이 대소문자 무시로 `ghostty`, `wezterm`, `herdr`
  - `KITTY_WINDOW_ID` 가 비어 있지 않음
  - `TERM=xterm-kitty`

  환경변수로 판단이 안 되면 시작할 때 kitty 그래픽 질의(`a=q`)를 보내고 최대 약 0.2초 응답을 기다린다. 응답이 없으면 다음 모드로 내려간다.
- `blocks`: `COLORTERM` 이 `truecolor` 또는 `24bit` 일 때. 반블록 `▀` 로 지도를 그린다.
- `text`: 점자 지도 + 라인 글자. 항상 동작하는 마지막 대안.

사용 중인 터미널이 이미지를 못 그리면 `-icons blocks` 를, 트루컬러도 없으면 `-icons text` 를 명시한다.
트루컬러 켜기: `export COLORTERM=truecolor` (대부분의 현대 터미널은 이미 설정돼 있다).

herdr: kitty 그래픽을 바깥 터미널로 넘기려면 herdr 설정의 `[terminal] kitty_graphics` 항목을 켠다 (https://herdr.dev/docs/configuration/). 바깥 터미널 자체가 kitty 프로토콜을 지원해야 한다.

## 6. 설치 검증 체크리스트

각 명령 뒤는 실제 실행 결과(일부 생략)다.

```bash
command -v esterm
# 기대: 설치된 경로, 예: /Users/<you>/.local/bin/esterm

esterm help
# 기대:
# esterm — esports terminal client
# Usage:
#   esterm                       interactive app (leagues, matches, live boards, replays)
#   esterm <game> <command> [flags]
# Games:
#   lol    League of Legends (https://lolesports.com endpoints)

esterm lol leagues | head
# 기대:
#   SLUG                   NAME                       ID
#   ----------------------------------------------------------------
#   americas_cup           Americas Cup               116096325848746167
#   arabian_league         Arabian League             109545772895506419

esterm lol demo -w 100 -h 40 | head -5
# 기대: 박스 테두리의 합성 보드
# ╔══════...══╗
# ║ ● LIVE  MSI · Knockouts                          ◷ 31:41  patch 16.13 ║
# ║ G2 Esports [2]                     BO5·G4                   [1] T1 ║

esterm lol demo -m -icons text | head -3
# 기대: 폭 80 의 미니맵 패널 레이아웃 (합성 좌표). 첫 줄이 ╔═…═╗ 로 시작

esterm lol relay -1
# 기대(라이브 경기가 없을 때):
# ║                                                   ○  NO LIVE GAMES RIGHT NOW ║
# ║                                                polling for a match to start… ║
# ║                                                     try: esterm lol schedule ║
```

대화형 앱 (사람이 터미널에서 확인): `esterm` 을 실행해 `[1] Leagues` 가 worlds 를 가리키고 Enter → `]` 로 Recent 탭 → 경기를 고르고
Enter 를 누르면 `[0]` 창에 `REPLAY` 보드와 맨 아래 플레이어 바가 나타나야 한다. `Space`, `←` `→`, `+`, `,`(옵션), `Esc`, `q` 를 차례로 눌러 본다.

미니맵 의존성을 설치했다면 (VOD 로 검증, 라이브 경기 불필요; 약 10~15초 소요):

```bash
esterm lol minimap -src "https://www.youtube.com/watch?v=UWBRuboiCXk" -ss 1500 -game 115660588583040711 -n 20 -json
# 기대: 정확히 20줄의 JSON (idx 0..19). 처음 약 5줄은 레이아웃을 찾는 중이라 {"idx":0,"present":false,"layout":"","clock":null,"kills":null,"visible":false,"champions":[]}
# 이후 줄: {"idx":19,"present":true,"layout":"worlds","clock":1141,"kills":[2,3],"visible":true,"champions":[{"pid":1,"champ":"Olaf","role":"top","team":100,"visible":true,"x":2378.3,"y":8740.8,"score":0.885},{"pid":2,"champ":"Vi","role":"jungle","team":100,"visible":false,"score":-2},...
```

`visible:false` 인 챔피언이 일부 있는 것은 정상이다(`score` -2 는 미검출).

## 7. 문제 해결

| 증상 | 원인 / 해결 |
|---|---|
| `zsh: command not found: esterm lol relay -l worlds` | 줄 전체가 하나의 단어로 읽힌 것. 복사 과정에서 공백이 non-breaking space(U+00A0)로 바뀐 경우다. 명령을 직접 다시 입력한다 |
| `command not found: esterm` | 설치 안 됨 또는 PATH 누락. 3장 수행 후 새 셸을 열거나 `hash -r` |
| `missing tools: …` | 4장 설치. 이름이 `deno\|bun\|node` 이면 JS 런타임 설치 |
| yt-dlp 가 JS 런타임 경고를 냄 | `deno` 설치, 또는 `bun`/`node` 를 PATH 에 둔다 (esterm 이 `--js-runtimes` 를 자동 지정) |
| `error: no live game; pass -game <gameId>` | 진행 중 경기가 없다. `-game <livestats gameId>` 를 직접 준다 |
| 상태 줄이 `sync: calibrating` | 방송 시계를 아직 5회 연속 읽지 못해 동기가 잠기지 않은 상태. 첫 잠금은 약 5초 걸린다. 오래 계속되면 리플레이/데스크 화면이거나 지원하지 않는 오버레이다 |
| 상태 줄이 `detecting broadcast layout` 에서 안 넘어감 | 공식 1080p 방송(worlds/lck/lpl/lec) 화면이 아니다(공동 시청 방송, 720p 등). 미니맵·점수판은 따로 재생되며 같은 순간이 아니다. 공식 방송 링크를 `-src` 로 준다 |
| 상태 줄에 `720p source` | 원본 스트림이 1080p 미만이라 방송 시계를 읽을 수 없다(Twitch `riotgames` 는 720p60 까지만 제공). 자동 선택이면 45초 뒤 다음 스트림으로 넘어간다. 직접 고를 때는 YouTube 공식 방송 링크를 `-src` 로 준다 |
| `video stalled Ns · …` | 영상이 8초 넘게 멎어 피드만 재생 중. 영상이 다시 흐르면 자동으로 재동기화한다. 계속되면 `v` 를 두 번 눌러 영상을 다시 시작 |
| kitty 이미지가 안 보임 | `-icons blocks` 로 폴백. herdr 라면 `[terminal] kitty_graphics` 확인 |
| 코드를 고쳤는데 동작이 그대로 | 오래된 바이너리다. `go build -o ~/.local/bin/esterm .` 로 다시 빌드하고 `hash -r` |
| 아이콘/아이템 이름이 이상하거나 오래됨 | Data Dragon 캐시 삭제 (아래) |

Data Dragon 캐시 위치: `os.UserCacheDir()/esterm/ddragon` (macOS `~/Library/Caches/esterm/ddragon`, Linux `~/.cache/esterm/ddragon`).
새 버전을 내려받으면 최신 2개 버전만 남기고 오래된 버전 폴더는 자동으로 지운다(버전당 수 MB).
삭제:

```bash
rm -rf ~/Library/Caches/esterm      # macOS
rm -rf ~/.cache/esterm              # Linux ($XDG_CACHE_HOME 가 있으면 그 아래)
```

## 8. 업데이트와 제거

```bash
# 업데이트
go install github.com/m16khb-org/esterm@latest                 # (a) 로 설치한 경우
cd <repo> && git pull && go build -o ~/.local/bin/esterm .      # (b) 로 설치한 경우

# 제거
rm -f "$(command -v esterm)"
rm -rf ~/Library/Caches/esterm ~/.cache/esterm                  # 캐시 (선택)
```
