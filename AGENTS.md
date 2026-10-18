# AGENTS.md

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
~~~text
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
~~~

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---

<!-- ISSUEOPS:START -->
## issueops project docs

This repository uses issueops project docs. Read existing AGENTS.md rules first, then read only the additional documents relevant to the task.

- Architecture or large design changes: .issueops/ARCHITECTURE.md, .issueops/CONSTITUTION.md
- Testing or verification changes: .issueops/TESTING.md
- Endpoint/DTO/OpenAPI changes: .issueops/OPEN_API_SPEC.md
- Commit or PR work: .issueops/COMMIT_POLICY.md
- Code style or structure changes: .issueops/CONVENTIONS.md
- Dependency or tech-stack changes: .issueops/TECH_STACK.md
- Run, deploy, environment, or local development: .issueops/OPERATIONS.md
- Agent start, verification, and completion workflow: .issueops/AGENT_WORKFLOW.md
- Risky or recurring-failure work: .issueops/CAUTIONS.md
- Structural rationale, alternatives, and decisions: .issueops/ADR.md
- Session start, instruction conflicts, and principle decisions: .issueops/CONSTITUTION.md
<!-- ISSUEOPS:END -->

