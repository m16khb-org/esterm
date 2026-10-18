---
name: 2026-10-18-nbsp-command-not-found
description: Caution record for a solved false case or recurring risk.
---

# 복사한 명령의 줄바꿈 없는 공백(NBSP) 때문에 command not found

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: esterm lol relay -l worlds 가 실행되지 않았다.
- Context: 붙여 넣은 명령에 non-breaking space가 섞여 셸이 'command not found: esterm lol relay -l worlds'로 한 덩어리를 명령 이름으로 읽었다.
- Resolution: 공백을 일반 공백으로 다시 입력한다. 문서의 명령 예시에는 일반 공백만 쓴다.
