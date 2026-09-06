package sdk

import (
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"runtime"
	"strings"
	"testing"
)

func linhasDe(registros ...map[string]any) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		for _, r := range registros {
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}
}

func reduzir(t *testing.T, d *Reduce, registros ...map[string]any) []map[string]any {
	t.Helper()
	if err := d.validate(); err != nil {
		t.Fatalf("validar: %v", err)
	}
	var out []map[string]any
	for env, err := range d.apply(linhasDe(registros...)) {
		if err != nil {
			t.Fatalf("reduce: %v", err)
		}
		out = append(out, env.Payload.(map[string]any))
	}
	return out
}

func vendas() []map[string]any {
	return []map[string]any{
		{"regiao": "sul", "ano": 2025.0, "valor": 10.0, "nome": "a", "ok": true},
		{"regiao": "sul", "ano": 2026.0, "valor": 30.0, "nome": "b", "ok": false},
		{"regiao": "norte", "ano": 2026.0, "valor": 20.0, "nome": "c", "ok": true},
		{"regiao": "sul", "ano": 2024.0, "valor": nil, "nome": "d", "ok": true},
	}
}

func TestAggregatorsComputeWhatTheyPromise(t *testing.T) {
	linhas := reduzir(t, &Reduce{
		By: GroupBy("regiao"),
		Agg: map[string]Aggregator{
			"linhas":     Count(),
			"com_valor":  CountOf("valor"),
			"total":      Sum("valor"),
			"media":      Mean("valor"),
			"menor":      Min("valor"),
			"maior":      Max("valor"),
			"primeiro":   First("nome"),
			"ultimo":     Last("nome"),
			"nome_final": MaxBy("nome", "ano"),
			"nome_velho": MinBy("nome", "ano"),
			"amplitude":  Range("valor"),
			"algum_ok":   Any("ok"),
			"todos_ok":   All("ok"),
		},
	}, vendas()...)

	if len(linhas) != 2 {
		t.Fatalf("saiu com %d grupos, esperado 2", len(linhas))
	}
	// Ordem determinística pela chave: "norte" antes de "sul".
	if linhas[0]["regiao"] != "norte" || linhas[1]["regiao"] != "sul" {
		t.Fatalf("ordem não determinística: %v", linhas)
	}

	sul := linhas[1]
	casos := map[string]any{
		"linhas": int64(3), "com_valor": int64(2),
		"total": 40.0, "media": 20.0, "menor": 10.0, "maior": 30.0,
		"primeiro": "a", "ultimo": "d",
		"nome_final": "b", "nome_velho": "d",
		"amplitude": 20.0, "algum_ok": true, "todos_ok": false,
	}
	for k, quero := range casos {
		if sul[k] != quero {
			t.Errorf("%s = %v (%T), esperado %v", k, sul[k], sul[k], quero)
		}
	}
}

// Um grupo sem nenhum valor devolve nulo, não zero. Zero é um número que
// alguém vai somar; nulo diz que não havia o que somar.
func TestAGroupWithNoValuesReturnsNullNotZero(t *testing.T) {
	linhas := reduzir(t, &Reduce{
		By:  GroupBy("g"),
		Agg: map[string]Aggregator{"total": Sum("v"), "media": Mean("v"), "n": Count()},
	}, map[string]any{"g": "x", "v": nil})

	if linhas[0]["total"] != nil || linhas[0]["media"] != nil {
		t.Errorf("total=%v media=%v, os dois deviam ser nulos", linhas[0]["total"], linhas[0]["media"])
	}
	if linhas[0]["n"] != int64(1) {
		t.Errorf("a linha existiu e Count devia vê-la: %v", linhas[0]["n"])
	}
}

// Welford: a fórmula ingênua perde todos os dígitos com valores grandes e
// próximos, e devolve variância NEGATIVA.
func TestVarianceSurvivesLargeValues(t *testing.T) {
	base := 1e9
	var registros []map[string]any
	for _, d := range []float64{0, 1, 2, 3, 4} {
		registros = append(registros, map[string]any{"g": "x", "v": base + d})
	}
	linhas := reduzir(t, &Reduce{
		By:  GroupBy("g"),
		Agg: map[string]Aggregator{"var": Variance("v"), "dp": StdDev("v")},
	}, registros...)

	v := linhas[0]["var"].(float64)
	if v < 0 {
		t.Fatalf("variância negativa (%v): a fórmula perdeu os dígitos", v)
	}
	if math.Abs(v-2.5) > 1e-6 {
		t.Errorf("variância = %v, esperado 2.5", v)
	}
	if dp := linhas[0]["dp"].(float64); math.Abs(dp-math.Sqrt(2.5)) > 1e-9 {
		t.Errorf("desvio = %v", dp)
	}
}

// Um campo com nome errado produziria uma coluna de nulos, e ninguém
// perceberia. Ele é recusado nomeando o que existe.
func TestAFieldNoRowHasIsRefused(t *testing.T) {
	d := &Reduce{By: GroupBy("regiao"), Agg: map[string]Aggregator{"total": Sum("vlaor")}}
	var erro error
	for _, err := range d.apply(linhasDe(vendas()...)) {
		if err != nil {
			erro = err
		}
	}
	if erro == nil {
		t.Fatal("um campo inexistente passou como coluna de nulos")
	}
	if !strings.Contains(erro.Error(), "vlaor") || !strings.Contains(erro.Error(), "valor") {
		t.Errorf("a mensagem precisa nomear o errado e listar os certos: %v", erro)
	}
}

// Tipos misturados no mesmo campo são erro: a ordem entre 10 e "9" dependeria
// da ordem de chegada, e o máximo mudaria entre execuções.
func TestMixedTypesAreAnError(t *testing.T) {
	d := &Reduce{By: GroupBy("g"), Agg: map[string]Aggregator{"maior": Max("v")}}
	var erro error
	for _, err := range d.apply(linhasDe(
		map[string]any{"g": "x", "v": 10.0},
		map[string]any{"g": "x", "v": "9"},
	)) {
		if err != nil {
			erro = err
		}
	}
	if erro == nil {
		t.Fatal("misturar número e texto passou")
	}
	if !strings.Contains(erro.Error(), "arrival order") {
		t.Errorf("a mensagem não diz o problema: %v", erro)
	}
}

// Um texto que é um número É um número: um CSV entrega tudo como texto, e
// recusá-lo obrigaria um transformer só para converter.
func TestNumericTextAddsUp(t *testing.T) {
	linhas := reduzir(t, &Reduce{
		By:  GroupBy("g"),
		Agg: map[string]Aggregator{"total": Sum("v")},
	},
		map[string]any{"g": "x", "v": "12.5"},
		map[string]any{"g": "x", "v": json.Number("2.5")},
	)
	if linhas[0]["total"] != 15.0 {
		t.Errorf("total = %v", linhas[0]["total"])
	}
}

// GroupBy() sem campos reduz o fluxo inteiro a uma linha -- o total geral.
func TestGroupByWithNoFieldsGivesTheGrandTotal(t *testing.T) {
	linhas := reduzir(t, &Reduce{
		Agg: map[string]Aggregator{"total": Sum("valor"), "n": Count()},
	}, vendas()...)
	if len(linhas) != 1 {
		t.Fatalf("saiu com %d linhas, esperado 1", len(linhas))
	}
	if linhas[0]["total"] != 60.0 || linhas[0]["n"] != int64(4) {
		t.Errorf("total geral errado: %v", linhas[0])
	}
}

// Finish vê os GRUPOS, não os registros -- é o que permite a fase global sem
// desfazer a garantia de memória.
func TestFinishSeesTheGroups(t *testing.T) {
	linhas := reduzir(t, &Reduce{
		By:  GroupBy("regiao"),
		Agg: map[string]Aggregator{"total": Sum("valor")},
		Finish: func(grupos iter.Seq2[Group, map[string]any]) ([]map[string]any, error) {
			var melhor map[string]any
			for g, row := range grupos {
				if len(g.Fields) != 1 || g.Fields[0] != "regiao" {
					return nil, fmt.Errorf("o grupo não trouxe seus campos: %v", g.Fields)
				}
				if melhor == nil || row["total"].(float64) > melhor["total"].(float64) {
					melhor = row
				}
			}
			return []map[string]any{melhor}, nil
		},
	}, vendas()...)

	if len(linhas) != 1 || linhas[0]["regiao"] != "sul" {
		t.Errorf("a redução global não escolheu o maior: %v", linhas)
	}
}

// As recusas falham na MONTAGEM, antes da extração: descobri-las depois
// custaria a janela do fornecedor.
func TestWhatDoesNotFitRefusesBeforeExtracting(t *testing.T) {
	for nome, a := range map[string]Aggregator{
		"Median":   Median("v"),
		"Quantile": Quantile("v", 0.9),
		"Distinct": Distinct("v"),
		"Mode":     Mode("v"),
		"Collect":  Collect("v"),
	} {
		err := (&Reduce{By: GroupBy("g"), Agg: map[string]Aggregator{"x": a}}).validate()
		if err == nil {
			t.Errorf("%s passou na validação", nome)
			continue
		}
		// A mensagem tem de dizer as duas saídas, porque elas existem.
		for _, esperado := range []string{"constant", "SQL", "sdk.Custom"} {
			if !strings.Contains(err.Error(), esperado) {
				t.Errorf("%s: a mensagem não diz %q: %v", nome, esperado, err)
			}
		}
	}
}

func TestANameCollidingWithTheGroupIsRefused(t *testing.T) {
	err := (&Reduce{
		By:  GroupBy("regiao"),
		Agg: map[string]Aggregator{"regiao": Count()},
	}).validate()
	if err == nil || !strings.Contains(err.Error(), "two values") {
		t.Errorf("colisão de nome passou: %v", err)
	}
}

// --- A prova que importa --------------------------------------------------

// gerarGrupos produz n registros distribuídos em g grupos, sem materializar
// nada: a origem tem de caber num fluxo, senão o teste mede a si mesmo.
func gerarGrupos(n, g int) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		for i := 0; i < n; i++ {
			r := map[string]any{
				"grupo": fmt.Sprintf("g%04d", i%g),
				"valor": float64(i % 1000),
				"nome":  "linha",
			}
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}
}

// picoDeHeap mede o maior heap vivo DURANTE o fold.
//
// Medir depois não serve, e isto custou uma versão do teste: quando a primeira
// linha sai, o estado dos grupos já está morto -- o `fechar` produziu a saída e
// o Go recolhe o resto -- então o `GC` apagava justamente o que se queria medir,
// e um agregador que guardava um milhão de linhas passava com 3 MB.
//
// A sonda entra como um agregador a mais: o `Add` dela roda uma vez por
// registro, enquanto TODO o estado dos grupos está vivo.
func picoDeHeap(t *testing.T, n, grupos int, agg map[string]Aggregator) uint64 {
	t.Helper()

	var pico uint64
	var i int
	intervalo := n / 20
	if intervalo < 1 {
		intervalo = 1
	}

	comSonda := make(map[string]Aggregator, len(agg)+1)
	for k, v := range agg {
		comSonda[k] = v
	}
	comSonda["_sonda"] = Custom(Accumulator{
		Init: func() any { return nil },
		Add: func(any, map[string]any) error {
			i++
			if i%intervalo != 0 {
				return nil
			}
			// GC antes de ler: sem ele a medida seria dominada pelo lixo dos
			// registros já processados, que é ruído, e não retenção.
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > pico {
				pico = m.HeapAlloc
			}
			return nil
		},
		Value: func(any) (any, error) { return nil, nil },
	})

	d := &Reduce{By: GroupBy("grupo"), Agg: comSonda}
	var linhas int
	for _, err := range d.apply(gerarGrupos(n, grupos)) {
		if err != nil {
			t.Fatal(err)
		}
		linhas++
	}
	if linhas != grupos {
		t.Fatalf("saiu com %d grupos, esperado %d", linhas, grupos)
	}
	return pico
}

// A promessa do Reduce não é "a soma está certa": é que a MEMÓRIA não cresce
// com a entrada.
//
// Cem grupos fixos, a entrada crescendo 100x. Se o teto se mantiver, a promessa
// está no código e não só na documentação -- e é esta a regressão mais provável
// desta feature: alguém acrescentar um agregador que guarda linhas.
func TestMemoryDoesNotGrowWithTheInput(t *testing.T) {
	if testing.Short() {
		t.Skip("mede heap com 1M de registros")
	}
	const grupos = 100
	agg := map[string]Aggregator{
		"n": Count(), "total": Sum("valor"), "media": Mean("valor"),
		"maior": Max("valor"), "dp": StdDev("valor"), "nome": MaxBy("nome", "valor"),
	}

	pequeno := picoDeHeap(t, 10_000, grupos, agg)
	grande := picoDeHeap(t, 1_000_000, grupos, agg)

	// Folga fixa, não proporcional: se a entrada fosse retida, 1M de registros
	// pesariam centenas de MB e estourariam qualquer folga razoável.
	const folga = 4 << 20
	if grande > pequeno+folga {
		t.Errorf("o heap cresceu com a entrada: %.1f MB com 10 mil registros, "+
			"%.1f MB com 1 milhão. A memória devia depender só dos %d grupos",
			mb(pequeno), mb(grande), grupos)
	}
}

// E a medição acima só vale se ela for capaz de PEGAR o defeito. Este
// agregador guarda as linhas -- exatamente o que a regra proíbe -- e o mesmo
// teto tem de reprová-lo.
func TestTheMeasurementCatchesAnAggregatorThatKeepsRows(t *testing.T) {
	if testing.Short() {
		t.Skip("mede heap")
	}
	const grupos = 100
	guardador := map[string]Aggregator{
		"tudo": Custom(Accumulator{
			Init: func() any { return &[]map[string]any{} },
			Add: func(acc any, r map[string]any) error {
				p := acc.(*[]map[string]any)
				*p = append(*p, r)
				return nil
			},
			Value: func(acc any) (any, error) { return len(*acc.(*[]map[string]any)), nil },
		}),
	}

	pequeno := picoDeHeap(t, 10_000, grupos, guardador)
	grande := picoDeHeap(t, 200_000, grupos, guardador)

	const folga = 4 << 20
	if grande <= pequeno+folga {
		t.Errorf("a medição NÃO pegou um agregador que guarda linhas: "+
			"%.1f MB contra %.1f MB. O teste da promessa não vale nada assim",
			mb(pequeno), mb(grande))
	}
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }
