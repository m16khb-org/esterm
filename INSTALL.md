# lolterm 설치 가이드 (에이전트용)

이 문서는 Claude Code, Codex, omp 같은 코딩 에이전트가 **사용자에게 되묻지 않고** 처음부터 끝까지 따라 할 수 있게 쓰였다.
모든 단계는 멱등(여러 번 실행해도 안전)이며, 각 단계에 확인 명령과 기대 출력이 있다.

## 에이전트용 요약

아래 순서로 실행하고, 분기는 "→" 를 따른다.

```bash
# 1. 사전 점검
go version                      # → go1.21 이상이 아니면: Go 설치(https://go.dev/dl/ 또는 macOS: brew install go) 후 재시도
git --version                   # → 없으면 설치 (소스 빌드를 쓸 때만 필요)

# 2. 설치 (둘 중 하나)
go install github.com/m16khb-org/lolterm@latest      # 바이너리: $(go env GOPATH)/bin/lolterm (원격에 푸시된 버전)
#   소스 체크아웃이 있으면 이쪽을 우선 (로컬 최신 기능 포함):  cd <repo> && mkdir -p ~/.local/bin && go build -o ~/.local/bin/lolterm .

# 3. PATH (없을 때만 추가)
BIN="$(go env GOPATH)/bin"          # 소스 빌드를 썼다면 BIN="$HOME/.local/bin"
case ":$PATH:" in *":$BIN:"*) ;; *) echo "export PATH=\"$BIN:\$PATH\"" >> ~/.zshrc; export PATH="$BIN:$PATH";; esac
#   bash 사용자는 ~/.zshrc 대신 ~/.bashrc

# 4. 기본 검증
command -v lolterm && lolterm lol demo -w 100 -h 40 | head -3
#   인자 없이 `lolterm` 을 실행하면 대화형 앱이 열린다 (q 로 종료). 에이전트의 비대화형 셸에서는 열지 말 것.

# 5. 최종 검증: 5장의 체크리스트
```

사용자 개입이 필요한 경우는 없다. 라이브 경기가 없을 때 `lolterm lol relay -1` 이 `NO LIVE GAMES RIGHT NOW` 를 출력하는 것은 정상이다.

## 1. 목적과 지원 OS

`lolterm` 은 라이브 LoL e스포츠 보드를 터미널에 그리는 Go 단일 바이너리다 (표준 라이브러리만 사용, 외부 모듈과 외부 도구 없음).

| OS | 상태 |
|---|---|
| macOS arm64 / amd64 | 기본 지원 |
| Linux | 빌드와 실행 가능. WSL2 Ubuntu 에서 `go test -race` 를 실행해 확인 |
| Windows 10 1809+ / 11 | 네이티브 빌드와 실행 가능. 의사 콘솔(ConPTY)에서 대화형 앱의 키 입력과 종료, `go test` 를 확인 |

raw 모드(키 입력)는 darwin, linux, windows 를 모두 지원한다 (`term_darwin.go` 는 `TIOCGETA/TIOCSETA`, `term_linux.go` 는 `TCGETS/TCSETS`, `term_windows.go` 는 콘솔 모드).
대화형 앱, `relay`, `replay` 의 키 입력(`b` `s` `o` `q`)은 세 OS 에서 모두 즉시 반응한다. Linux 는 빌드만 확인했고, Linux 터미널에서 대화형 앱을 직접 실행해 보지는 않았다.

## 2. 사전 점검

```bash
go version
# 기대: go version go1.21 이상 (go.mod 의 `go 1.21`). 예: go version go1.27.2 darwin/arm64
git --version            # 소스 빌드에만 필요
```

네트워크 (2개 호스트). 루트 경로는 403 을 돌려주므로 실제 경로로 확인한다:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -H "x-api-key: 0TvQnueqKa5mxJntVWt0w4LpLfEkrV1Ta8rQBb9Z" "https://esports-api.lolesports.com/persisted/gw/getLeagues?hl=en-US"   # 기대: 200
curl -s -o /dev/null -w "%{http_code}\n" "https://feed.lolesports.com/livestats/v1/window/115660588583040711"                                                  # 기대: 200
```

(`curl -sI https://<host>/` 는 403 이 나온다. 정상이며 연결 가능하다는 뜻이다. API 키는 lolesports.com 이 쓰는 공용 키이며 별도 발급은 필요 없다.)

PATH 점검:

```bash
command -v lolterm || echo "lolterm 이 PATH 에 없음 → 3장 PATH 설정"
```

## 3. 설치

### (a) go install

```bash
go install github.com/m16khb-org/lolterm@latest
# 설치 위치: $(go env GOBIN) 가 설정돼 있으면 그곳, 아니면 $(go env GOPATH)/bin
# 주의: @latest 는 원격 저장소에 올라간 버전이다. 로컬 체크아웃에만 있는 변경이 필요하면 (b) 소스 빌드를 쓴다.
```

### (b) 소스 빌드

```bash
git clone git@github.com:m16khb-org/lolterm.git && cd lolterm     # 또는 https://github.com/m16khb-org/lolterm.git
mkdir -p ~/.local/bin
go build -o ~/.local/bin/lolterm .
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

### Windows (PowerShell)

```powershell
go install .                       # 저장소 안에서. %USERPROFILE%\go\bin\lolterm.exe, Go 설치 프로그램이 PATH 에 넣어 둔 경로
lolterm
# 또는: go build . 후 .\lolterm.exe   (PowerShell 은 현재 폴더의 실행 파일을 .\ 없이 찾지 않는다)
```

콘솔은 가상 터미널 입출력(ANSI)을 켜서 쓴다.

### macOS quarantine

브라우저로 내려받은 바이너리를 실행할 때 Gatekeeper 가 막는 경우에만 해당한다. `go install`/`go build` 로 만든 바이너리에는 필요 없다.

```bash
xattr -d com.apple.quarantine "$(command -v lolterm)" 2>/dev/null || true
```

## 4. 터미널 설정

별도 설정은 필요 없다. 화면은 최소 40×12, 보드는 50열 이상이 필요하다. 대화형 화면은 표준 입출력이 터미널일 때만 열리고, 파이프나 리디렉션으로 실행하면 오류로 끝난다.

## 5. 설치 검증 체크리스트

각 명령 뒤는 실제 실행 결과(일부 생략)다.

```bash
command -v lolterm
# 기대: 설치된 경로, 예: /Users/<you>/.local/bin/lolterm

lolterm help
# 기대:
# lolterm — esports terminal client
# Usage:
#   lolterm                       interactive app (leagues, matches, live boards, replays)
#   lolterm <game> <command> [flags]
# Games:
#   lol    League of Legends (https://lolesports.com endpoints)

lolterm lol leagues | head
# 기대:
#   SLUG                   NAME                       ID
#   ----------------------------------------------------------------
#   americas_cup           Americas Cup               116096325848746167
#   arabian_league         Arabian League             109545772895506419

lolterm lol demo -w 100 -h 40 | head -5
# 기대: 박스 테두리의 합성 보드
# ╔══════...══╗
# ║ ● LIVE  MSI · Knockouts                          ◷ 31:41  patch 16.13 ║
# ║ G2 Esports [2]                     BO5·G4                   [1] T1 ║

lolterm lol relay -1
# 기대(라이브 경기가 없을 때):
# ║                                                   ○  NO LIVE GAMES RIGHT NOW ║
# ║                                                polling for a match to start… ║
# ║                                                     try: lolterm lol schedule ║
```

대화형 앱 (사람이 터미널에서 확인): `lolterm` 을 실행해 `[1] Leagues` 가 worlds 를 가리키고 Enter → `]` 로 Recent 탭 → 경기를 고르고
Enter 를 누르면 `[0]` 창에 `REPLAY` 보드와 맨 아래 플레이어 바가 나타나야 한다. `Space`, `←` `→`, `+`, `,`(옵션), `Esc`, `q` 를 차례로 눌러 본다.

## 6. 문제 해결

| 증상 | 원인 / 해결 |
|---|---|
| `zsh: command not found: lolterm lol relay -l worlds` | 줄 전체가 하나의 단어로 읽힌 것. 복사 과정에서 공백이 non-breaking space(U+00A0)로 바뀐 경우다. 명령을 직접 다시 입력한다 |
| `command not found: lolterm` | 설치 안 됨 또는 PATH 누락. 3장 수행 후 새 셸을 열거나 `hash -r` |
| `waiting for in-game data…` | 진행 중으로 표시된 경기에 방송(spectate)이 아직 시작되지 않아 피드가 비어 있다. 정상이며 방송이 시작되면 자동으로 이어진다 |
| 코드를 고쳤는데 동작이 그대로 | 오래된 바이너리다. `go build -o ~/.local/bin/lolterm .` 로 다시 빌드하고 `hash -r` |

## 7. 업데이트와 제거

```bash
# 업데이트
go install github.com/m16khb-org/lolterm@latest                 # (a) 로 설치한 경우
cd <repo> && git pull && go build -o ~/.local/bin/lolterm .      # (b) 로 설치한 경우

# 제거
rm -f "$(command -v lolterm)"
```
