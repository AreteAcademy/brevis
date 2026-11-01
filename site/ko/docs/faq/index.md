# FAQ

> 가장 먼저 나오는 질문들에 대한 짧은 답변과 다음에 볼 곳입니다.

*https://brevis.sh/ko/docs/faq/ · brevis.sh docs (ko)*

---

## brevis.sh는 정말 100% 오픈 소스인가요?

네. 전체 런타임을 읽고, 사용하고, 수정하고, 기여할 수 있도록 공개되어 있습니다.

## 세네카에 대한 언급은 프로젝트에 어떤 의미가 있나요?

De Brevitate Vitae는 더 의도적인 엔지니어링에 영감을 줍니다. 시간을 잡아먹는 복잡성은 줄이고, 목적 있는 일을 위한 여유는 지키자는 것입니다.

자세한 내용은 [철학](/ko/docs/philosophy/index.md)에 있습니다.

## pod 격리는 어떻게 동작하나요?

workflow의 각 step은 자기만의 이미지를 가진 자기만의 pod로 실행되어, 의존성과 실행 컨텍스트가 분리됩니다.

자세한 내용은 [step당 pod 하나](/ko/docs/pod-per-step/index.md)에 있습니다.

## Aretê Academy와는 어떤 관계인가요?

brevis.sh는 Aretê Academy의 프로젝트입니다. Aretê Academy는 철학의 훌륭한 아이디어를 소프트웨어 개발의 실제 과제에 더 가까이 가져옵니다.

## 구성 요소 중 하나만 사용할 수 있나요?

네. 각 구성 요소는 고유한 모듈, 버전, 이미지를 가집니다. SDK는 어떤 프로그램에서든 실행되는 Go 라이브러리이고, Gateway는 YAML 파일 하나만 있으면 데이터베이스 없이 단독으로 시작하며, brevis-sql은 컨테이너로 실행됩니다. 원할 때 Core가 나머지를 오케스트레이션하지만, 어느 구성 요소도 Core를 전제 조건으로 요구하지 않습니다.

자세한 내용은 [에코시스템](/ko/docs/ecosystem/index.md)에 있습니다.
