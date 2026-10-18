---
name: overview
description: Family module overview: installation and runtime operation.
---

# Operations — Overview

Canonical index: [OPERATIONS.md](../../OPERATIONS.md)

## 빌드와 설치

- 소스 빌드: `go build -o ~/.local/bin/esterm .` (INSTALL.md). 원격 버전: `go install github.com/m16khb-org/esterm@latest`. 로컬에만 있는 변경은 소스 빌드로만 반영된다.
- 사용자용 절차는 README.md와 INSTALL.md가 우선이다. 이 문서는 요약과 운영 주의만 둔다.

## 실행과 스모크 확인

- 네트워크 없이: `esterm lol demo -w 100 -h 40 | head -3`.
- 라이브 경기 한 장면: `esterm lol relay -1` (경기가 없으면 `NO LIVE GAMES RIGHT NOW`가 정상이다).
- 전체 화면 보드: `esterm lol relay -l <league>`. 키는 `b`/`s`/`o` 패널, `m` 미니맵 패널 표시, `v` 방송 영상 동기화 켜기/끄기, `q` 종료.
- 미니맵 단독 검증(VOD): `esterm lol minimap -src <URL|파일> -ss <초> -game <livestats gameId> -n <프레임 수> -json`. 출력 필드는 `layout`, `clock`, `kills`, `visible`, `present`, `champions[]`다. 정확한 필드는 코드에서 확인한다.

## 외부 도구(미니맵에만 필요)

- `ffmpeg`, `yt-dlp`, JS 런타임 `deno`/`bun`/`node` 중 하나. 없으면 나머지 기능은 그대로 동작한다.
- 경로 재지정 환경 변수: `ESTERM_FFMPEG`, `ESTERM_YTDLP` (`minimap_video.go`의 `mmTool`).
- 로컬 VOD 스모크에서 도구가 PATH에 없으면 실행할 때 PATH를 앞에 덧붙인다. 도구 위치는 개발 머신마다 다르다.

## 캐시

- Data Dragon 이미지와 버전은 `os.UserCacheDir()/esterm/ddragon`에 저장된다(`ddragon.go`). 지워도 다음 실행 때 다시 받는다.

## 환경 변수와 비밀

- 시크릿은 쓰지 않는다. lolesports API 키는 사이트가 공개하는 공용 키다. 그 외 환경 변수는 위 두 가지와 터미널 판별용 `COLORTERM`, `TERM_PROGRAM`, `KITTY_WINDOW_ID`, `TERM` 읽기뿐이다(`minimap_kitty.go`).

## 배포

- CI 워크플로(`.github`)는 없다. 배포 절차는 Unknown / not confirmed이며, 확인하려면 저장소 원격 설정과 릴리스 관행을 사용자에게 물어야 한다.

## Project docs bootstrap and upkeep

- `issueops project bootstrap --repo . --json` creates docs and user-state repo metadata; `--sync` refreshes them from current evidence.
- After initial setup, agents should read repo evidence and keep `.issueops` docs current through MCP `project_docs_route` → `project_docs_read` → `project_docs_revise`.
- Append resolved false cases and decisions to CAUTIONS/ADR with `project_docs_append` instead of rewriting full documents.

## Context hooks

- The issueops installer connects `issueops hook session-start` to the host SessionStart event; it renders the static project-doc catalog, including after compaction.
- `issueops hook subagent-start` gives each starting subagent the same catalog, except Explore, explorer, and fork agents.
- Hosts without a SessionStart re-run after compaction use `issueops hook post-compact` for the same catalog. `ISSUEOPS_DISABLE_HOOKS=1` turns every hook into a no-op.
- The hook does not execute work, use the network, or read large files.
