# 기여 가이드

lolterm에 관심을 가져 주셔서 고맙습니다. 버그 제보, 문서 수정, 기능 제안, 코드 기여 모두 환영합니다.

## 시작하기

1. 저장소를 포크하고 로컬에 클론합니다.
2. Go 1.21 이상으로 빌드되는지 확인합니다.
3. 변경 사항을 작은 커밋으로 나눠 PR을 엽니다.

```bash
git clone https://github.com/<your-name>/lolterm.git
cd lolterm
go test -count=1 ./...
go build -o ~/.local/bin/lolterm .
```

미니맵 관련 작업을 하려면 `yt-dlp`, `ffmpeg`, JS 런타임(`deno`, `bun`, `node` 중 하나)이 필요합니다.
의존성과 PATH 설정은 [INSTALL.md](INSTALL.md)를 참고하세요.

## 테스트

```bash
go test -count=1 ./...                 # 단위 테스트
go test -tags backtest -run TestBacktest -timeout 3h -v . \
  -backtest.out=/tmp/backtest.json     # 방송 영상 백테스트 (선택)
```

- 동작을 바꾸는 변경에는 회귀 테스트를 함께 추가합니다.
- UI 변경은 스크린샷이나 짧은 영상을 PR 본문에 첨부합니다.
- 미니맵 변경은 사용한 경기 ID, 세트 번호, 재현 시각을 적습니다.
- 테스트를 실행하지 못했다면 PR 본문에 이유와 확인한 범위를 적습니다.

## 코드 스타일

- `gofmt`를 적용합니다.
- 기존 구조와 이름을 따르고, 한 번만 쓰는 추상화는 만들지 않습니다.
- 새 의존성은 표준 라이브러리로 해결할 수 없는 이유를 PR에 적습니다.
- 사용자에게 보이는 문구를 추가하면 `i18n.go`의 한국어·영어·중국어를 함께 채웁니다.

## 커밋

Conventional Commits 형식을 사용합니다.

```text
feat(minimap): ...
fix(replay): ...
docs(readme): ...
test(feed): ...
```

본문에는 왜 바꿨는지, 무엇을 확인했는지, 확인하지 못한 부분을 적습니다.

## 이슈

버그를 제보할 때는 다음 정보를 포함해 주세요.

- 경기 ID와 세트 번호
- 사용한 명령과 `-icons` 모드
- 터미널 종류와 크기
- 재현 시각과 기대한 동작
- 가능하면 스크린샷이나 로그

보안 문제는 공개 이슈 대신 저장소 소유자에게 비공개로 알려 주세요.

## 라이선스

기여한 코드는 저장소와 같은 GPL-3.0-or-later 라이선스로 배포됩니다.
