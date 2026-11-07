---
name: 2026-11-07-youtube-ffmpeg-https-dash-url-403-hls
description: Caution record for a solved false case or recurring risk.
---

# YouTube는 ffmpeg가 여는 https(DASH) 영상 URL에 403을 준다: HLS 형식을 고른다

- Date: 2026-11-07
- Kind: `caution`
- Source: cli
- Summary: yt-dlp가 고른 1080p avc1 https 형식(299)의 videoplayback URL을 ffmpeg가 열면 곧바로 403 Forbidden이 났다. 같은 영상의 HLS 형식(312, m3u8_native)은 열리고 -ss 탐색도 빠르다. 미니맵과 다시 보기 영상, 백테스트 다운로드가 모두 이 경로를 쓴다.
- Context: 릴리스 전 백테스트 준비 중 VOD 다운로드가 0바이트에서 멈췄다. curl/urllib Range 요청과 yt-dlp http_headers를 붙인 ffmpeg도 403이었다. 감사 때 본 minimap -src YouTube 403도 같은 원인이었고 일시적 rate limit이 아니었다. HLS를 matroska로 -c copy 하면 'Can't write packet with unknown timestamp'로 실패했다.
- Resolution: mmYTFormat 앞에 bv*[vcodec^=avc1][height<=1080][protocol^=m3u8]를 두었다(minimap -src 실 YouTube 40프레임: worlds 잠금, 시계 판독, 챔피언 8~9명 검출 확인). 백테스트 클립은 mp4로 저장하고, HLS URL에는 dur 파라미터가 없어 영상 길이를 yt-dlp --print duration으로 얻는다. YouTube 형식 선택을 바꿀 때는 실제 VOD로 ffmpeg 열기와 탐색을 직접 확인한다.
