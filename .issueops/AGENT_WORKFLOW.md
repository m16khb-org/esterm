---
name: AGENT_WORKFLOW.md
description: Agent start, execution, verification, and completion flow; read when starting or handing off a task.
---

# Agent Workflow

## Start

1. Read AGENTS.md first.
2. At session start, treat .issueops/CONSTITUTION.md as the baseline principle document.
3. If MCP is available, send the current task to project_docs_route and select only necessary docs.
4. Verify inferred doc claims against current files and command output.

## MCP usage rule

- At session start the installed SessionStart hook (issueops hook session-start) injects the project-doc catalog. It is context for judgment, not an auto-execution command.
- Use MCP when the task needs current state, repo-specific doc routing, policy decisions, state checkpoints, or durable records that the model should not rely on from memory.
- Do not use MCP for simple reasoning or summarizing already opened files.
- Avoid exposing many tools at once; narrowly use route/read/revise/append/check tools that match the task.
- Do not trust tool output blindly; check paths, exists flags, warnings, and verification evidence.

## Work

Use the Simplicity First and Surgical Changes principles from AGENTS.md, plus these project append/safety rules.

- Do not overwrite existing user changes.
- Add dependencies, deploy, or perform destructive actions only with explicit instruction or strong evidence.
- If docs diverge from current code or user consensus, use project_docs_read to verify the current SHA and project_docs_revise to change one document at a time.
- When a problem occurred and was resolved, record it with MCP project_docs_append(kind=caution) as a dated file under .issueops/cautions/.
- When a structural decision or rejected alternative matters, record it with MCP project_docs_append(kind=adr) as a dated file under .issueops/adr/.

## Verify

Use the Goal-Driven Execution principle from AGENTS.md, plus these verification routing rules.

- Before writing or modifying tests, read the good/bad test criteria in .issueops/TESTING.md.
- When changing CLI/MCP/API documentation contracts, also run golden/schema/smoke verification.
- Completion reports must include test/build/static-check results and reasons for skipped verification.

## Finish

- If a commit is needed, follow .issueops/COMMIT_POLICY.md.
- Record resolved false cases or structural decisions with MCP project_docs_append when useful.
