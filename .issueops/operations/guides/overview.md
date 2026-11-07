---
name: overview
description: Family module overview: installation and runtime operation.
---

# Operations — Overview

Canonical index: [OPERATIONS.md](../../OPERATIONS.md)

## 빌드와 설치

- 소스 빌드: `go build -o ~/.local/bin/lolterm .` (INSTALL.md). 원격 버전: `go install github.com/m16khb-org/lolterm@latest`. 로컬에만 있는 변경은 소스 빌드로만 반영된다.
- 사용자용 절차는 README.md와 INSTALL.md가 우선이다. 이 문서는 요약과 운영 주의만 둔다.

## 실행과 스모크 확인

- 네트워크 없이: `lolterm lol demo -w 100 -h 40 | head -3`.
- 라이브 경기 한 장면: `lolterm lol relay -1` (경기가 없으면 `NO LIVE GAMES RIGHT NOW`가 정상이다).
- 전체 화면 보드: `lolterm lol relay -l <league>`. 키는 `b`/`s`/`o` 패널, `q` 종료. 끝난 경기는 `lolterm lol replay -match <id> [-g N]`.

## 외부 도구

- 필요 없다. Go 표준 라이브러리만 쓰고 외부 프로세스를 실행하지 않는다.

## 캐시와 디스크

- lolterm 은 디스크에 아무것도 저장하지 않는다. 아이템·룬 이름은 `staticdata.go`에 내장되어 있고, replay 가 받은 프레임은 메모리에만 둔다(최근 3게임).

## 환경 변수와 비밀

- 시크릿은 쓰지 않는다. lolesports API 키는 사이트가 공개하는 공용 키다. 읽는 환경 변수는 UI 언어를 정하는 `LOLTERM_LANG`뿐이다(`-lang`으로도 정한다).

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
