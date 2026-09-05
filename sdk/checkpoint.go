package sdk

import (
	"context"
	"fmt"
	"hash/fnv"
	"iter"
	"log/slog"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/checkpoint"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Checkpoint guarda o extract bruto desta execucao para que uma SEGUNDA
// TENTATIVA da mesma run nao precise consultar a origem de novo.
//
//	sdk.Run(sdk.Pipeline{
//		Source:     /* ... */,
//		Checkpoint: sdk.Checkpoint{At: "gs://landing/_checkpoint", Store: gcs.New(c)},
//		Target:     /* ... */,
//	})
//
// O que ele compra e a quota do fornecedor. Um extract que gastou 4.803
// requisicoes e quarenta minutos nao deveria ser refeito porque uma coluna do
// destino mudou de tipo.
//
// O que ele custa: a extracao deixa de ser uma passada unica. O extract
// inteiro pousa no deposito antes de a primeira linha ser carregada, e depois
// e relido de la -- uma escrita e uma leitura a mais do volume, em TODA
// execucao, para socorrer a que falha. Por isso vem desligado.
//
// # Quando NAO usar
//
// Se o extract e o load ja sao dois passos do DAG, isto nao acrescenta nada: o
// motor ja tenta cada passo de novo separadamente, entao um load que falha nao
// refaz o extract, que e outro no que teve sucesso. Escreva com to.Files, leia
// Result.Objects, e passe adiante. E menos codigo e da dois nos na tela em vez
// de um.
//
// # Garantias
//
//   - Um deposito incompleto NUNCA e retomado. O manifesto e escrito por
//     ultimo; sem ele o extract e refeito.
//   - Um deposito so serve a run que o escreveu. Nada e reaproveitado entre
//     execucoes, entao nao ha dado velho entrando como novo.
//   - Os ingestion_id de uma retomada sao IDENTICOS aos da primeira tentativa.
//   - Falhar ao gravar o deposito nao derruba a execucao: ela segue e avisa.
type Checkpoint struct {
	// At e o diretorio raiz dos depositos. Vazio desliga.
	//
	// O caminho efetivo leva a run e o pipeline por baixo dele, porque uma run
	// tem varios passos e dois passos nao podem dividir o mesmo deposito.
	At string

	// Store e o backend de object storage, como nos drivers. Nil e o disco
	// local.
	Store core.Store
}

// estadoCheckpoint e o que aconteceu com o deposito nesta execucao. Os campos
// sao preenchidos enquanto o fluxo corre, e lidos depois que ele termina.
type estadoCheckpoint struct {
	caminho       string
	reaproveitado bool
	erro          string
}

func (e *estadoCheckpoint) aplicar(r *Result) {
	if e == nil || r == nil {
		return
	}
	r.CheckpointPath = e.caminho
	r.CheckpointReused = e.reaproveitado
	r.CheckpointError = e.erro
}

// caminhoDoCheckpoint monta o deposito desta execucao:
//
//	{At}/{run_id}/{nome}-{hash}/
//
// A run identifica a execucao e o nome identifica o passo. O hash esta ai
// porque o nome vira um segmento de caminho e precisa ser saneado: sem ele
// dois pipelines cujos nomes sanitizam para a mesma coisa dividiriam o
// deposito, e um retomaria do extract do outro -- dado errado carregado em
// silencio, que e o pior jeito de falhar.
func (p *Pipeline) caminhoDoCheckpoint() string {
	nome := p.name()
	h := fnv.New32a()
	_, _ = h.Write([]byte(nome))
	return fmt.Sprintf("%s/%s/%s-%08x/",
		strings.TrimSuffix(p.Checkpoint.At, "/"), segmento(p.Run.ID), segmento(nome), h.Sum32())
}

// segmento deixa um texto livre virar um pedaco de caminho.
func segmento(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "pipeline"
	}
	return b.String()
}

// extrairComCheckpoint e o Extract do runPipeline, com o deposito no meio.
func extrairComCheckpoint(ctx context.Context, p *Pipeline) (*Data, *estadoCheckpoint, error) {
	est := &estadoCheckpoint{}

	if p.Checkpoint.At == "" {
		d, err := Extract(ctx, p.Source)
		return d, est, err
	}

	// Sem run id nao ha chave estavel: cada execucao escreveria num lugar
	// diferente e nada seria reaproveitado nunca. Dizer e melhor que ignorar
	// em silencio -- alguem configurou isto esperando que funcionasse.
	if p.Run.ID == "" {
		slog.WarnContext(ctx, "checkpoint desligado: sem "+core.EnvRunID+
			" nao ha chave estavel entre tentativas",
			"pipeline", p.name(), "checkpoint", p.Checkpoint.At)
		d, err := Extract(ctx, p.Source)
		return d, est, err
	}

	// Configuracao errada e ERRO, nao aviso: um esquema que nao casa com o
	// Store nunca vai gravar, e seguir avisando esconderia isso em toda
	// execucao ate o dia em que alguem precisasse retomar.
	dep, err := checkpoint.Novo(p.caminhoDoCheckpoint(), p.Checkpoint.Store)
	if err != nil {
		return nil, est, err
	}
	est.caminho = dep.Caminho()

	// Na primeira tentativa nao ha o que retomar -- o caminho leva o id da run,
	// e esta run comeca aqui. Nao procurar evita um aviso por execucao dizendo
	// que nao achou o que nao podia existir.
	if p.Run.Attempt > 0 {
		if d, ok := retomar(ctx, p, dep, est); ok {
			return d, est, nil
		}
	}

	data, err := Extract(ctx, p.Source)
	if err != nil {
		return nil, est, err
	}

	// Provar que da para gravar ANTES de gastar a quota. A falha mais comum e
	// permissao, e descobri-la depois da extracao significaria ter gasto
	// exatamente aquilo que o checkpoint existe para poupar.
	if err := dep.Reservar(ctx, p.name(), p.Run.ID); err != nil {
		est.erro = err.Error()
		slog.WarnContext(ctx, "checkpoint indisponivel; a execucao segue sem ele",
			"pipeline", p.name(), "checkpoint", est.caminho, "erro", err)
		return data, est, nil
	}

	data.Records = materializar(ctx, dep, data.Records, p, est)
	return data, est, nil
}

// retomar le o deposito da tentativa anterior, quando ele esta inteiro.
func retomar(ctx context.Context, p *Pipeline, dep *checkpoint.Deposito,
	est *estadoCheckpoint) (*Data, bool) {

	m, err := dep.Manifesto(ctx)
	if err != nil {
		slog.InfoContext(ctx, "sem checkpoint utilizavel; refazendo o extract",
			"pipeline", p.name(), "checkpoint", est.caminho, "motivo", err)
		return nil, false
	}
	if err := dep.Conferir(ctx, m); err != nil {
		slog.WarnContext(ctx, "checkpoint incompleto; refazendo o extract",
			"pipeline", p.name(), "checkpoint", est.caminho, "motivo", err)
		return nil, false
	}

	est.reaproveitado = true
	slog.InfoContext(ctx, "checkpoint reaproveitado: a origem nao sera consultada",
		"pipeline", p.name(), "checkpoint", est.caminho,
		"registros", m.Registros, "tentativa", p.Run.Attempt)

	stats := p.Source.Stats
	if stats == nil {
		stats = &core.Stats{}
	}
	return &Data{
		Records: dep.Reler(ctx, m),
		source:  p.Source,
		start:   time.Now(),
		// Paginas e tentativas ficam em zero, e e a verdade: esta execucao nao
		// buscou pagina nenhuma.
		stats: stats,
	}, true
}

// materializar drena a origem para o deposito e devolve o que foi gravado.
//
// A releitura nao e desperdicio de teste: ela faz o caminho da RETOMADA rodar
// em toda execucao bem-sucedida. Um caminho de recuperacao que so roda em
// emergencia e um caminho que ninguem nunca viu funcionar.
func materializar(ctx context.Context, dep *checkpoint.Deposito,
	origem iter.Seq2[Envelope, error], p *Pipeline, est *estadoCheckpoint) iter.Seq2[Envelope, error] {

	nome, run := p.name(), p.Run.ID

	return func(yield func(Envelope, error) bool) {
		esc := dep.Escrever()
		proximo, parar := iter.Pull2(origem)
		defer parar()

		// degradar desiste do deposito sem desistir da execucao: cede o que ja
		// virou objeto, o que ficou no buffer, e segue direto da origem. Nao
		// escreve manifesto, entao ninguem retoma de um deposito capenga.
		degradar := func(causa error, pendente *Envelope) {
			est.erro = causa.Error()
			slog.WarnContext(ctx, "checkpoint interrompido; a execucao segue sem ele",
				"pipeline", nome, "checkpoint", est.caminho, "erro", causa)

			for env, err := range dep.Reler(ctx, esc.Gravadas()) {
				if !yield(env, err) {
					return
				}
			}
			for env, err := range esc.Pendentes() {
				if !yield(env, err) {
					return
				}
			}
			if pendente != nil && !yield(*pendente, nil) {
				return
			}
			for {
				env, err, ok := proximo()
				if !ok {
					return
				}
				if !yield(env, err) {
					return
				}
			}
		}

		for {
			env, err, ok := proximo()
			if !ok {
				break
			}
			if err != nil {
				// A origem falhou: o extract nao terminou, e sem manifesto
				// ninguem vai retomar deste deposito pela metade.
				yield(Envelope{}, err)
				return
			}
			if e := esc.Add(env); e != nil {
				degradar(e, &env) // nao entrou no buffer, entao vai a mao
				return
			}
			if esc.Cheio() {
				if e := esc.Despejar(ctx); e != nil {
					degradar(e, nil) // ja esta no buffer; Pendentes o cede
					return
				}
			}
		}

		if err := esc.Fechar(ctx, nome, run); err != nil {
			degradar(err, nil)
			return
		}

		for env, err := range dep.Reler(ctx, esc.Gravadas()) {
			if !yield(env, err) {
				return
			}
		}
	}
}
