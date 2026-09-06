package sdk

import (
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Reduce agrega o fluxo antes de ele chegar ao destino.
//
//	Reduce: &sdk.Reduce{
//		By: sdk.GroupBy("regiao", "ano"),
//		Agg: map[string]sdk.Aggregator{
//			"linhas":     sdk.Count(),
//			"total":      sdk.Sum("valor"),
//			"nome_final": sdk.MaxBy("nome", "ano"),
//		},
//	}
//
// Ele fica ENTRE o Transform e o Target: os transformers preparam a linha, o
// fold reduz, o destino recebe o resultado.
//
// # A regra que decide o que existe aqui
//
// Todo agregador deste pacote usa memoria CONSTANTE por grupo. Nao e
// preferencia: e a unica regra que preserva a promessa do SDK, cujo modelo e um
// fluxo. Um agregador que guardasse as linhas desfaria isso em silencio, e o
// sintoma apareceria como um pod morto por falta de memoria as cinco da manha.
//
// Com a regra, o custo e previsivel e dizivel:
//
//	memoria = numero de grupos x estado dos agregadores
//
// A ENTRADA nao aparece nessa conta. By isso Median, Quantile, Distinct
// exato, Mode e Collect nao existem -- e a mensagem de quem procurar por eles
// diz as duas saidas que existem.
type Reduce struct {
	// By sao os campos que formam a chave do grupo. GroupBy() sem campos
	// reduz o fluxo inteiro a uma linha so.
	By Grouping

	// Agg sao as colunas calculadas, pelo nome que elas terao na saida.
	Agg map[string]Aggregator

	// Finish roda uma passada sobre os GRUPOS depois que o fluxo acaba, para
	// uma reducao global, uma junção com uma tabela pequena, ou uma projecao
	// final.
	//
	// Ele ve os grupos, nunca os registros -- e e isso que permite a fase
	// global sem desfazer a garantia: a memoria continua proporcional ao
	// numero de grupos.
	Finish func(grupos iter.Seq2[Group, map[string]any]) ([]map[string]any, error)
}

// Group e a chave de um grupo, com os campos na ordem de GroupBy.
type Group struct {
	Fields []string
	Values map[string]any
}

// Grouping sao os campos que formam a chave.
type Grouping struct{ campos []string }

// GroupBy nomeia os campos da chave do grupo, na ordem em que aparecem.
//
// Sem campos, o fluxo inteiro vira um grupo so -- que e como se pede um total
// geral.
func GroupBy(campos ...string) Grouping { return Grouping{campos: campos} }

// Accumulator e a porta de baixo: o que fazer quando os agregadores prontos nao
// cobrem o caso.
//
// Os agregadores deste pacote sao construidos com ela, o que garante que a
// porta funciona -- e nao e uma saida de emergencia que ninguem testou.
type Accumulator struct {
	// Init cria o estado de um grupo novo.
	Init func() any

	// Add incorpora um registro ao estado.
	Add func(acc any, r map[string]any) error

	// Value fecha o estado na coluna de saida.
	Value func(acc any) (any, error)
}

// Aggregator e uma coluna calculada. Use os construtores deste arquivo, ou
// Custom para o que eles nao cobrem.
type Aggregator struct {
	acc Accumulator

	// recusa e o motivo de este agregador nao poder existir. Ver o fim deste
	// arquivo.
	recusa error

	// campos sao os do registro que este agregador le, para que um nome
	// errado seja recusado nomeando -- e nao produza um total zero.
	campos []string
}

// Custom embrulha um Accumulator.
//
// A memoria e SUA a partir daqui: um estado que cresce com as linhas desfaz a
// garantia do Reduce, e o SDK nao tem como conferir isso por voce.
func Custom(a Accumulator) Aggregator {
	return Aggregator{acc: a}
}

// --- Os agregadores -------------------------------------------------------

// Count conta as linhas do grupo.
func Count() Aggregator {
	return Custom(Accumulator{
		Init:  func() any { return new(int64) },
		Add:   func(acc any, _ map[string]any) error { *acc.(*int64)++; return nil },
		Value: func(acc any) (any, error) { return *acc.(*int64), nil },
	})
}

// CountOf conta as linhas em que o campo nao e nulo.
func CountOf(campo string) Aggregator {
	a := Custom(Accumulator{
		Init: func() any { return new(int64) },
		Add: func(acc any, r map[string]any) error {
			if r[campo] != nil {
				*acc.(*int64)++
			}
			return nil
		},
		Value: func(acc any) (any, error) { return *acc.(*int64), nil },
	})
	return comCampos(a, campo)
}

// Sum soma o campo. Nulo e ausente sao ignorados, como no SQL.
func Sum(campo string) Aggregator {
	type estado struct {
		total float64
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numeroDe(r, campo)
			if err != nil || !ok {
				return err
			}
			e := acc.(*estado)
			e.total, e.viu = e.total+n, true
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*estado)
			if !e.viu {
				return nil, nil // grupo sem nenhum valor: nulo, nao zero
			}
			return e.total, nil
		},
	})
	return comCampos(a, campo)
}

// Mean e a media aritmetica do campo, ignorando nulos.
func Mean(campo string) Aggregator {
	type estado struct {
		soma float64
		n    int64
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numeroDe(r, campo)
			if err != nil || !ok {
				return err
			}
			e := acc.(*estado)
			e.soma, e.n = e.soma+n, e.n+1
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*estado)
			if e.n == 0 {
				return nil, nil
			}
			return e.soma / float64(e.n), nil
		},
	})
	return comCampos(a, campo)
}

// Min e o menor valor do campo. Max e o maior.
func Min(campo string) Aggregator { return extremo(campo, -1) }

// Max e o maior valor do campo.
func Max(campo string) Aggregator { return extremo(campo, +1) }

func extremo(campo string, sinal int) Aggregator {
	type estado struct {
		valor any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			v := r[campo]
			if v == nil {
				return nil
			}
			e := acc.(*estado)
			if !e.viu {
				e.valor, e.viu = v, true
				return nil
			}
			cmp, err := comparar(v, e.valor, campo)
			if err != nil {
				return err
			}
			if cmp*sinal > 0 {
				e.valor = v
			}
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*estado).valor, nil },
	})
	return comCampos(a, campo)
}

// First e o primeiro valor nao nulo visto no grupo. Last e o ultimo.
//
// "First" e na ordem em que a origem entregou: para uma origem sem ordem
// definida, ele nao e determinista, e isso e da origem, nao daqui.
func First(campo string) Aggregator { return pontaDo(campo, true) }

// Last e o ultimo valor nao nulo visto no grupo.
func Last(campo string) Aggregator { return pontaDo(campo, false) }

func pontaDo(campo string, primeiro bool) Aggregator {
	type estado struct {
		valor any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			v := r[campo]
			if v == nil {
				return nil
			}
			e := acc.(*estado)
			if primeiro && e.viu {
				return nil
			}
			e.valor, e.viu = v, true
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*estado).valor, nil },
	})
	return comCampos(a, campo)
}

// MinBy devolve o `valor` da linha em que `chave` e minima. MaxBy, a maxima.
//
//	sdk.MaxBy("nome", "ano")   // o nome da linha de maior ano
//
// E o que normalmente falta e faz alguem guardar as linhas para depois
// escolher -- que e justamente o que a regra da memoria constante proibe.
func MinBy(valor, chave string) Aggregator { return porExtremo(valor, chave, -1) }

// MaxBy devolve o `valor` da linha em que `chave` e maxima.
func MaxBy(valor, chave string) Aggregator { return porExtremo(valor, chave, +1) }

func porExtremo(campoValor, campoChave string, sinal int) Aggregator {
	type estado struct {
		chave any
		valor any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			k := r[campoChave]
			if k == nil {
				return nil
			}
			e := acc.(*estado)
			if !e.viu {
				e.chave, e.valor, e.viu = k, r[campoValor], true
				return nil
			}
			cmp, err := comparar(k, e.chave, campoChave)
			if err != nil {
				return err
			}
			if cmp*sinal > 0 {
				e.chave, e.valor = k, r[campoValor]
			}
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*estado).valor, nil },
	})
	return comCampos(a, campoValor, campoChave)
}

// Variance e a variancia amostral do campo. StdDev e a raiz dela.
//
// By Welford, numa passada: a formula ingenua (soma dos quadrados menos o
// quadrado da soma) perde todos os digitos significativos quando os valores sao
// grandes e proximos entre si, e o resultado sai negativo.
func Variance(campo string) Aggregator { return welford(campo, false) }

// StdDev e o desvio padrao amostral do campo.
func StdDev(campo string) Aggregator { return welford(campo, true) }

func welford(campo string, raiz bool) Aggregator {
	type estado struct {
		n    float64
		medi float64
		m2   float64
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			x, ok, err := numeroDe(r, campo)
			if err != nil || !ok {
				return err
			}
			e := acc.(*estado)
			e.n++
			d := x - e.medi
			e.medi += d / e.n
			e.m2 += d * (x - e.medi)
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*estado)
			if e.n < 2 {
				return nil, nil // variancia amostral de um ponto nao existe
			}
			v := e.m2 / (e.n - 1)
			if raiz {
				return math.Sqrt(v), nil
			}
			return v, nil
		},
	})
	return comCampos(a, campo)
}

// Any e verdadeiro quando alguma linha tem o campo verdadeiro. All, quando
// todas tem.
func Any(campo string) Aggregator { return booleano(campo, false) }

// All e verdadeiro quando todas as linhas tem o campo verdadeiro.
func All(campo string) Aggregator { return booleano(campo, true) }

func booleano(campo string, todos bool) Aggregator {
	type estado struct {
		v   bool
		viu bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{v: todos} },
		Add: func(acc any, r map[string]any) error {
			v := r[campo]
			if v == nil {
				return nil
			}
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("o campo %q vale %v (%T), e não um booleano; "+
					"Any e All leem booleanos", campo, v, v)
			}
			e := acc.(*estado)
			e.viu = true
			if todos {
				e.v = e.v && b
			} else {
				e.v = e.v || b
			}
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*estado)
			if !e.viu {
				return nil, nil
			}
			return e.v, nil
		},
	})
	return comCampos(a, campo)
}

// Range e a diferenca entre o maior e o menor valor do campo.
func Range(campo string) Aggregator {
	type estado struct {
		min, max float64
		viu      bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &estado{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numeroDe(r, campo)
			if err != nil || !ok {
				return err
			}
			e := acc.(*estado)
			if !e.viu {
				e.min, e.max, e.viu = n, n, true
				return nil
			}
			e.min, e.max = math.Min(e.min, n), math.Max(e.max, n)
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*estado)
			if !e.viu {
				return nil, nil
			}
			return e.max - e.min, nil
		},
	})
	return comCampos(a, campo)
}

func comCampos(a Aggregator, campos ...string) Aggregator {
	a.campos = campos
	return a
}

// --- Coercao --------------------------------------------------------------

// numeroDe le um campo como numero. O segundo retorno diz se havia valor:
// nulo e ausente sao ignorados, como no SQL.
//
// Um texto que e um numero E um numero -- `12.5` vindo de um CSV nao e
// ambiguo, e recusa-lo obrigaria um transformer so para converter. O que nao e
// numero vira erro nomeando o campo E o valor, porque sem o valor ninguem acha
// a linha culpada num milhao.
func numeroDe(r map[string]any, campo string) (float64, bool, error) {
	v := r[campo]
	switch t := v.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return t, true, nil
	case float32:
		return float64(t), true, nil
	case int:
		return float64(t), true, nil
	case int64:
		return float64(t), true, nil
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false, fmt.Errorf("o campo %q vale %q, que não é um número", campo, t.String())
		}
		return f, true, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false, fmt.Errorf("o campo %q vale %q, que não é um número", campo, t)
		}
		return f, true, nil
	default:
		return 0, false, fmt.Errorf("o campo %q vale %v (%T), que não é um número", campo, v, v)
	}
}

// comparar ordena dois valores do mesmo campo. Numeros por valor, textos por
// ordem lexical.
//
// Tipos misturados sao ERRO, e nao uma ordem inventada: um campo que traz 10 e
// "9" faria o maximo depender da ordem de chegada, e o resultado mudaria entre
// execucoes sem ninguem perceber.
func comparar(a, b any, campo string) (int, error) {
	na, aNum := comoNumero(a)
	nb, bNum := comoNumero(b)
	if aNum && bNum {
		switch {
		case na < nb:
			return -1, nil
		case na > nb:
			return 1, nil
		}
		return 0, nil
	}

	sa, aTxt := a.(string)
	sb, bTxt := b.(string)
	if aTxt && bTxt {
		return strings.Compare(sa, sb), nil
	}

	return 0, fmt.Errorf("o campo %q mistura %T e %T no mesmo grupo; "+
		"a ordem entre eles dependeria da ordem de chegada", campo, a, b)
}

// comoNumero e a coercao SEM texto: aqui um "9" e texto, e comparar textos com
// numeros e o erro que a funcao acima recusa.
func comoNumero(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

// --- O fold ---------------------------------------------------------------

// separadorDeGrupo junta os campos da chave. O byte zero nao aparece em texto
// vindo de JSON nem de CSV, entao dois grupos distintos nao colidem por causa
// de um valor que contem o separador.
const separadorDeGrupo = "\x00"

func (d *Reduce) validate() error {
	if d == nil {
		return nil
	}
	if len(d.Agg) == 0 && d.Finish == nil {
		return fmt.Errorf("Reduce sem Agg e sem Finish não faz nada; " +
			"remova-o, ou diga o que ele calcula")
	}
	for _, campo := range d.By.campos {
		if _, colide := d.Agg[campo]; colide {
			return fmt.Errorf("%q é campo do grupo e nome de agregador ao mesmo tempo; "+
				"a coluna teria dois valores", campo)
		}
	}
	for _, nome := range nomesOrdenados(d.Agg) {
		a := d.Agg[nome]
		// A recusa vem ANTES da extracao: descobri-la depois significaria ter
		// gasto a quota do fornecedor para nada.
		if a.recusa != nil {
			return fmt.Errorf("em %q: %w", nome, a.recusa)
		}
		if a.acc.Init == nil || a.acc.Add == nil || a.acc.Value == nil {
			return fmt.Errorf("o agregador %q está incompleto: Custom precisa "+
				"de Init, Add e Value", nome)
		}
	}
	return nil
}

type grupoAcumulado struct {
	chave   string
	valores map[string]any
	estados map[string]any
	ordem   int
}

// apply drena o fluxo, agrega e devolve as linhas resultantes.
func (d *Reduce) apply(linhas iter.Seq2[Envelope, error]) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		grupos, vistos, err := d.dobrar(linhas)
		if err != nil {
			yield(Envelope{}, err)
			return
		}
		if err := d.conferirCampos(vistos); err != nil {
			yield(Envelope{}, err)
			return
		}

		saida, err := d.fechar(grupos)
		if err != nil {
			yield(Envelope{}, err)
			return
		}
		for _, row := range saida {
			if !yield(Envelope{Payload: row}, nil) {
				return
			}
		}
	}
}

func (d *Reduce) dobrar(linhas iter.Seq2[Envelope, error]) ([]*grupoAcumulado, map[string]bool, error) {
	porChave := map[string]*grupoAcumulado{}
	var ordem []*grupoAcumulado
	vistos := map[string]bool{}
	// A identidade e conferida na PRIMEIRA linha: se ela chegou aqui, chegou em
	// todas, e conferir uma vez custa nada num milhao.
	primeira := true

	for env, err := range linhas {
		if err != nil {
			return nil, nil, err
		}
		row, err := comoRegistro(env.Payload)
		if err != nil {
			return nil, nil, err
		}
		if primeira {
			primeira = false
			if err := refuseIdentity(row); err != nil {
				return nil, nil, err
			}
		}
		for k := range row {
			vistos[k] = true
		}

		chave, valores, err := d.chaveDe(row)
		if err != nil {
			return nil, nil, err
		}
		g := porChave[chave]
		if g == nil {
			g = &grupoAcumulado{
				chave: chave, valores: valores,
				estados: make(map[string]any, len(d.Agg)),
				ordem:   len(ordem),
			}
			for nome, a := range d.Agg {
				g.estados[nome] = a.acc.Init()
			}
			porChave[chave] = g
			ordem = append(ordem, g)
		}
		for nome, a := range d.Agg {
			if err := a.acc.Add(g.estados[nome], row); err != nil {
				return nil, nil, fmt.Errorf("agregador %q: %w", nome, err)
			}
		}
	}

	// Ordem determinística pela chave do grupo. A identidade da linha vem do
	// conteúdo, então a ordem não muda o resultado -- mas um -sample que
	// devolve linhas diferentes a cada execução atrapalha quem depura.
	sort.Slice(ordem, func(i, j int) bool { return ordem[i].chave < ordem[j].chave })
	return ordem, vistos, nil
}

// conferirCampos recusa um agregador que nomeia um campo que NENHUMA linha
// tinha.
//
// Sem isto, um nome com erro de digitação produz uma coluna de nulos ou zeros e
// ninguém percebe -- que é o pior jeito de falhar. Um campo ausente em ALGUMAS
// linhas continua sendo normal, e é ignorado como no SQL.
func (d *Reduce) conferirCampos(vistos map[string]bool) error {
	if len(vistos) == 0 {
		return nil // fluxo vazio: não há o que conferir
	}
	faltando := map[string]bool{}
	for _, a := range d.Agg {
		for _, c := range a.campos {
			if !vistos[c] {
				faltando[c] = true
			}
		}
	}
	for _, c := range d.By.campos {
		if !vistos[c] {
			faltando[c] = true
		}
	}
	if len(faltando) == 0 {
		return nil
	}
	nomes := chavesOrdenadas(faltando)
	return fmt.Errorf("o Reduce nomeia %s, que nenhuma linha tem. Os campos disponíveis são: %s",
		strings.Join(aspas(nomes), ", "), strings.Join(chavesOrdenadas(vistos), ", "))
}

func (d *Reduce) chaveDe(row map[string]any) (string, map[string]any, error) {
	if len(d.By.campos) == 0 {
		return "", map[string]any{}, nil
	}
	var b strings.Builder
	valores := make(map[string]any, len(d.By.campos))
	for i, campo := range d.By.campos {
		if i > 0 {
			b.WriteString(separadorDeGrupo)
		}
		v := row[campo]
		valores[campo] = v
		b.WriteString(asText(v))
	}
	return b.String(), valores, nil
}

func (d *Reduce) fechar(grupos []*grupoAcumulado) ([]map[string]any, error) {
	linhas := make([]map[string]any, 0, len(grupos))
	for _, g := range grupos {
		row := make(map[string]any, len(g.valores)+len(d.Agg))
		for k, v := range g.valores {
			row[k] = v
		}
		for nome, a := range d.Agg {
			v, err := a.acc.Value(g.estados[nome])
			if err != nil {
				return nil, fmt.Errorf("agregador %q: %w", nome, err)
			}
			row[nome] = v
		}
		linhas = append(linhas, row)
	}

	if d.Finish == nil {
		return linhas, nil
	}
	return d.Finish(func(yield func(Group, map[string]any) bool) {
		for i, g := range grupos {
			if !yield(Group{Fields: d.By.campos, Values: g.valores}, linhas[i]) {
				return
			}
		}
	})
}

func comoRegistro(p any) (map[string]any, error) {
	row, ok := p.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("o Reduce agrega objetos JSON; veio %T", p)
	}
	return row, nil
}

// nomesOrdenados torna as mensagens de erro estáveis: sem isto, um pipeline com
// dois agregadores inválidos reclamaria de um diferente a cada execução.
func nomesOrdenados(m map[string]Aggregator) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func chavesOrdenadas(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func aspas(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strconv.Quote(v)
	}
	return out
}

// --- O que não existe, e por quê ------------------------------------------
//
// Estes quatro existem como FUNÇÃO e recusam na montagem, antes da extração.
//
// Não é o mesmo que não existir. Quem escreve `sdk.Median("x")` e recebe
// "undefined" do compilador vai implementá-la à mão -- guardando as linhas do
// grupo, que é exatamente o que a regra da memória constante existe para
// impedir. Quem recebe a mensagem abaixo fica sabendo POR QUE, e as duas saídas
// que existem.

// Median não existe: ela precisa de todas as linhas do grupo.
//
// Calcule no destino, com SQL, ou use Custom e assuma o custo de memória
// explicitamente.
func Median(campo string) Aggregator { return recusar("Median", "todas as linhas do grupo") }

// Quantile não existe, pelo mesmo motivo de Median.
func Quantile(campo string, q float64) Aggregator {
	return recusar("Quantile", "todas as linhas do grupo")
}

// Distinct não existe: a contagem exata precisa de um conjunto por grupo, que
// cresce com a cardinalidade da entrada.
func Distinct(campo string) Aggregator { return recusar("Distinct", "um conjunto por grupo") }

// Mode não existe: ela precisa de um mapa de frequências por grupo.
func Mode(campo string) Aggregator { return recusar("Mode", "um mapa de frequências por grupo") }

// Collect não existe: juntar as linhas do grupo é literalmente o que a regra
// proíbe.
func Collect(campo string) Aggregator { return recusar("Collect", "todas as linhas do grupo") }

func recusar(nome, custo string) Aggregator {
	return Aggregator{recusa: fmt.Errorf(
		"sdk.%s não existe: ela precisa de %s, e este agregador roda em memória "+
			"constante. Duas saídas: calcule no destino, com SQL, ou use "+
			"sdk.Custom -- e assuma o custo de memória explicitamente", nome, custo)}
}
