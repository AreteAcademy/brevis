# Filosofia

> De onde vem o brevis.sh: Sêneca, a Aretê Academy, e o que isso decide no código.

*https://brevis.sh/docs/philosophy/ · brevis.sh docs (pt-BR)*

---

O brevis.sh nasce no grupo Aretê Academy: disciplina, propósito e excelência aplicados à construção de software.

## De brevitate vitae

> Não é que temos pouco tempo. É que perdemos muito dele.

A máxima de Sêneca guia a pergunta por trás do brevis.sh: quais complexidades realmente merecem o tempo de uma equipe? Cada workflow executa em pods isolados para que falhas, versões e dependências tenham limites claros.

Quatro escolhas do runtime saem dessa pergunta:

- **imagem por etapa** — Cada passo declara o runtime de que precisa. Trocar a versão de um não obriga a mexer nos outros.
- **logs por execução** — A saída pertence ao run que a produziu. Investigar uma falha não é reconstruir um contexto perdido.
- **retries persistentes** — A tentativa sobrevive ao processo. Uma reinicialização não apaga o que já se sabia sobre o trabalho.
- **isolamento de dependências** — O que uma etapa carrega não vaza para as vizinhas. Limite claro é o que torna a falha legível.

## A proposta do projeto

Entre *filosofia sem prática* e *tecnologia sem direção*, **clareza para fazer o que importa.**

## Ferramentas moldam a forma como pensamos.

> “Código sustentável não é apenas código que continua rodando. É código que deixa espaço para as pessoas raciocinarem.” — Filosofia aplicada ao software

> “A clareza não é o oposto da sofisticação. É o critério que impede a sofisticação de se tornar ruído.” — Open source por princípio
