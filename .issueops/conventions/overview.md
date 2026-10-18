---
name: overview
description: Family module overview: implementation and interface conventions.
---

# Conventions — Overview

Canonical index: [CONVENTIONS.md](../CONVENTIONS.md)

## 확인된 규칙

- 표준 라이브러리만 쓴다. 모듈을 추가하지 않는다(go.mod에 require가 없고 README.md가 "의존성 없음"을 밝힌다).
- 문법은 go 1.21 기준으로 쓴다. go.mod의 `go 1.21`을 따르며 `for i := range n` 같은 신규 문법은 쓰지 않고 고전적인 for 문을 쓴다.
- `gofmt`로 정리한다. 저장소 전체 포맷은 요청받았을 때만 한다.
- 문서(README.md, INSTALL.md, `.issueops` 보강)는 한국어, 코드·식별자·명령은 영어 그대로 둔다.
- 방송 프레임이나 캡처를 저장소에 넣지 않는다.
- 죽은 코드, 자리 표시자, TODO를 남기지 않는다. 교체한 코드의 옛 경로는 지운다.
- 에러는 값으로 반환한다(`getJSON` 등의 `error` 반환). 계층형 구조나 인터페이스 추상화는 외부 경계(HTTP, 자식 프로세스)에만 있다.

## Editing rules

- Follow existing style first.
- Do not run repo-wide formatting unless explicitly requested.
- Add new dependencies only after documenting the need and alternatives.

## SOLID / Design Pattern guidance

Apply SOLID, YAGNI, and KISS together. SOLID does not mean adding interfaces and layers by default; it means clarifying responsibility and dependency direction only where a real axis of change exists. Use a design pattern only when the name explains the problem and reduces maintenance cost.

### Good cases

- Apply existing patterns such as Adapter, Strategy, Factory, or Repository consistently to the same kind of problem.
- Put interfaces/ports at boundaries with real substitutability, such as external hosts, SDKs, filesystems, processes, or networks.
- Use dependency inversion where there are multiple implementations or a test double is needed.
- When introducing a pattern, record the problem, chosen pattern, rejected simpler alternatives, and cost in ADR.md.

### Bad cases

- Creating an interface, factory, registry, or plugin layer for a single use site.
- Adding abstraction or configurability based only on hypothetical future extension.
- Expanding a simple function call into a class/object graph just to match a pattern name.
- Duplicating core policy in host adapters or per-host implementations in the name of SOLID.

### Rules

- Start with the simplest implementation; introduce patterns only after a real variation point is confirmed.
- Add a new abstraction only when there are at least two use sites, a clear test boundary, or an external technology boundary.
- If a pattern turns a 50-line solution into a 200-line structure, revert and simplify.

## Engineering standards checklist

Standard topics the enrichment pass must evaluate against repository
evidence before writing them into docs (catalog:
project-docs-bootstrap/references/engineering-standards.md):

- Architecture style actually used: layered, hexagonal (ports and adapters), onion, clean architecture, modular monolith, microservices.
- DDD: bounded contexts, ubiquitous language, entities/value objects/aggregates/repositories.
- SOLID, OOP, composition over inheritance, clean-code conventions (naming, function size, error handling).
- Error/exception handling style: errors as values vs exceptions, wrapping, fail-fast boundaries, HTTP error contract.
- API documentation: OpenAPI/Swagger coverage of parameters and client-handled error statuses.
- Testing best practices: test pyramid/sizes, test doubles, determinism, behavior-focused assertions.

Rules:

- Confirm each topic from source files, config, commands, or explicit user instruction before documenting it.
- Write confirmed topics to the single owning document listed in the catalog's topic-to-doc map.
- Record adoption or rejection of a style or major practice in ADR.md with rationale.
- Omit or mark unconfirmed topics as "Unknown / not confirmed"; do not paste generic advice.
