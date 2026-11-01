# 화이트 라벨

> YAML 파일 하나로 인터페이스의 제목, 모토, 로고, 팔레트를 바꿉니다.

*https://brevis.sh/ko/docs/white-label/ · brevis.sh docs (ko)*

---

인터페이스는 파일로 사용자 지정할 수 있습니다. 색상은 **런타임에** CSS 변수를
덮어쓰므로, 고객의 테마를 바꿔도 다시 컴파일할 것이 없습니다.

## 파일

`brand.example.yaml`을 루트의 `brand.yaml`로 복사하거나, `BREVIS_BRAND_FILE`이 그
파일을 가리키게 하세요.

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**모든 필드는 선택 사항입니다.** 생략한 항목은 기본값을 유지하므로, `titulo:`만 있는
파일도 이미 유효합니다.

`frase`의 줄바꿈은 보존됩니다. 사이드바에서 문구의 리듬을 결정하기 때문입니다.

## 로고

```yaml
logo: https://example.com/mark.svg
```

URL(`https://` 또는 `http://`)이나 `/`로 시작하는 내부 경로를 받습니다. 없으면 테마
색상을 따르는 내장 심볼을 사용합니다.

:::warning 다른 곳에 호스팅한 로고는 그 호스트에 의존합니다
호스트가 다운되었거나 클러스터에서 인터넷으로 나가는 경로가 없으면 이미지가 로드되지
않습니다. 인터페이스의 나머지는 계속 동작하지만, 선택할 때 이를 고려하세요. 폰트와
번들을 포함한 UI의 나머지는 네트워크에 의존하지 않도록 바이너리 자체에서 제공됩니다.
:::

## 검증

잘못된 16진수 값은 컨테이너가 올라올 때에야 드러나고, 메시지는 파일을 수정한 사람과
멀리 떨어진 pod 로그를 통해 도착합니다. 그래서 하위 명령이 있습니다.

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

설치 환경의 CI에서 호출하면 오류가 pull request에서 돌아옵니다.

:::note 파일이 없다는 것의 의미는 상황에 따라 다릅니다
`serve`에서 파일이 없다는 것은 "기본 아이덴티티를 사용"한다는 뜻이며 API는 정상적으로
올라옵니다. `marca`에서 파일이 없는 것은 **오류**입니다. 경로를 검증해 달라고 요청한
사람은 그 경로가 존재하지 않는다는 사실을 알려 주기를 기대하기 때문입니다.
:::

## 부팅 시 동작

내용 오류, 즉 잘못된 색상이나 깨진 YAML은 인터페이스가 올라오는 것을 **막지 않습니다**.
프로세스는 경고를 로그에 남기고 기본 테마를 사용합니다. 색상 하나 때문에 API를
내리는 것은 기본 테마로 제공하는 것보다 나쁩니다.

```
level=WARN msg="identidade visual ignorada" arquivo=brand.yaml erro="cor invalida: #gggggg"
```

## 사용자 지정할 수 없는 것

푸터의 **"Powered by Brevis"** 문구는 구성에서 오지 않고 코드에서 옵니다.

## Kubernetes에서

ConfigMap으로 파일을 마운트하세요.

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
