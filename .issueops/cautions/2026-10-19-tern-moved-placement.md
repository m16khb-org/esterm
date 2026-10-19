---
name: 2026-10-19-tern-moved-placement
description: Caution record for a solved false case or recurring risk.
---

# Windows Tern은 같은 배치 번호로 다시 놓은 kitty 이미지를 그리지 않는다

- Date: 2026-10-19
- Kind: `caution`
- Source: cli
- Summary: 사용자의 Windows Tern 창(맥 Tern 데몬에서 중계, herdr 안팎 모두)에서 지도·용/바론 아이콘·타이머 숫자는 보였지만 움직이는 챔피언 아이콘은 하나도 보이지 않았다.
- Context: 같은 스프라이트가 움직이지 않는 `esterm lol demo -m` 에서는 보였다. replay 는 0.1초마다 `a=p,i=I,p=P` 로 이미 놓인 배치를 다른 칸에 다시 놓았다. 맥에서 돌린 `tern shot`(일반, `remote loopback`, `remote rtt`, 창 크기 변경)과 kitty 출력 합성에서는 모두 보여 로컬에서 재현되지 않았다. 맥에서는 화면 캡처 권한이 없고 하빈 PC 로 SSH 가 닫혀 있어 사용자 화면으로만 확인했다.
- Resolution: minimap_kitty.go `kittySpritePID`: 챔피언마다 배치 번호 두 개(i+1, kittyAltPID+i+1)를 번갈아 써서, 옮길 때 새 번호로 놓고 예전 배치를 지운다(글리프 바꿀 때와 같은 순서). 실험 빌드를 herdr 패널과 Tern 탭에서 사용자가 보임으로 확인했다(2026-10-19). 테스트: TestKittySpriteMovesNeverReplaceTheirOwnPlacement. 지도 타일·오브젝트 아이콘은 슬롯 원점이 바뀔 때만 같은 번호로 다시 놓으므로 그대로 두었다(드문 경우라 미검증).
