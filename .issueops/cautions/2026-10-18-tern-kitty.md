---
name: 2026-10-18-tern-kitty
description: Caution record for a solved false case or recurring risk.
---

# Tern 원격 중계에서 kitty 이미지가 빠지거나 찌그러진다

- Date: 2026-10-18
- Kind: `caution`
- Source: cli
- Summary: herdr 안의 Tern 원격 창에서 큰 지도 이미지와 맨 먼저 보낸 이미지가 사라지고, 픽셀 오프셋을 준 아이콘이 타원으로 그려졌다.
- Context: 사용자 환경은 Tern(원격 창, 데몬 중계) 안의 herdr 0.9.5. herdr 는 지도를 정상으로 내보냈고 로컬 tern shot 에서도 보였지만 원격 창에서는 한 장짜리 지도(RGBA 약 900KB)가 안 보였고, 타일로 나눈 뒤에는 첫 타일(좌상단)만 빠졌다. 같은 id 를 다른 크기로 다시 보내면 그림이 사라졌고(재현), X/Y 오프셋만큼 Tern 이 배치 상자를 줄여 원이 찌그러졌다(측정: 오프셋별 76x64~77x34).
- Resolution: minimap_kitty.go: 지도를 64KB 이하 타일로 나눠 보내고, 실행마다 무작위 id 블록을 쓰고, 흐린 백킹 이미지를 맨 먼저 보내 첫 이미지가 빠져도 지도가 온전하게 했다. 아이콘은 X/Y 오프셋 없이 칸에 맞춰 놓고, 이미지마다 커서 이동 직후 a=T 로 전송과 배치를 함께 한다. 사용자 원격 창에서 해결 확인(2026-10-18). Tern 의 중계 캐시 상한값은 확인하지 못했다(Unknown).
