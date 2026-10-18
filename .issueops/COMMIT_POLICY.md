---
name: COMMIT_POLICY.md
description: Commit message format and scope rules; read before committing.
---

# Commit Policy

## Default

- Prefer small atomic commits.
- Run verification appropriate to the change scope before committing.
- Use Conventional Commit format unless the project has stricter rules.

~~~text
<type>(<scope>): <summary>

Why: <why this change exists>
Tested: <commands run>
Not-tested: <known verification gaps>
~~~

## Safety

- Do not stage unrelated changes.
- Manually inspect secret-like paths or credential changes before committing.
