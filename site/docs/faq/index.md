# FAQ

> As perguntas que chegam primeiro, com respostas curtas e para onde ir depois.

*https://brevis.sh/docs/faq/ · brevis.sh docs (pt-BR)*

---

## O brevis.sh é realmente 100% open source?

Sim. O runtime completo é aberto para leitura, uso, adaptação e contribuição da comunidade.

## O que a referência a Sêneca significa para o projeto?

De Brevitate Vitae inspira uma engenharia mais intencional: reduzir a complexidade que consome tempo e preservar espaço para trabalho com propósito.

Mais em [Filosofia](/docs/philosophy/index.md).

## Como funciona o isolamento dos pods?

Cada etapa do workflow executa como um pod próprio com sua imagem, mantendo dependências e contexto de execução separados.

Mais em [Pod por passo](/docs/pod-per-step/index.md).

## Qual é a relação com a Aretê Academy?

O brevis.sh é um projeto do grupo Aretê Academy, que aproxima grandes ideias da filosofia dos desafios reais da construção de software.

## Posso usar só uma das peças?

Pode. Cada peça tem módulo, versão e imagem próprios: o SDK é uma biblioteca Go que roda em qualquer programa, o Gateway sobe sozinho com um YAML e sem banco, e o brevis-sql roda como um container. O Core orquestra as outras quando você quer — não é pré-requisito de nenhuma.

Mais em [Ecossistema](/docs/ecosystem/index.md).
