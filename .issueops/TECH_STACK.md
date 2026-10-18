---
name: TECH_STACK.md
description: Chosen languages, runtimes, and tools; read before adding a dependency or tool.
---

# Tech Stack

## Detected languages

- Go

## 버전

- Go 언어 버전: `go 1.21` (go.mod). 관찰된 툴체인: `go version go1.27.2 darwin/arm64`.
- 모듈 의존성 없음. 패키지 매니저 파일은 go.mod뿐이며 외부 모듈을 추가하지 않는다.
- 외부 실행 도구(선택, 미니맵): `ffmpeg`, `yt-dlp`, JS 런타임 `deno`/`bun`/`node`. 버전 고정은 Unknown / not confirmed. 확인: `ffmpeg -version`, `yt-dlp --version`.
- 지원 OS: macOS 기본, Linux 빌드 가능, Windows 미확인(INSTALL.md).

## Evidence files

- go.mod
- README.md
- INSTALL.md
- minimap_video.go
