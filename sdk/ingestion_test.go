package sdk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

func aplica(t *testing.T, fn Transformer, in map[string]any) map[string]any {
	t.Helper()
	got, err := fn(in)
	if err != nil {
		t.Fatalf("transformer: %v", err)
	}
	return got.(map[string]any)
}

// The criterion that holds everything: the id has to be the one the block used
// to produce.
// Se ele mudar, toda carga anterior de todo consumidor deixa de casar.
//
// The expected value comes from Envelope.IngestionID, which is the
// implementation that
// existia antes e continua conferida byte a byte contra o uuid.uuid5 do
// Python.
func TestIngestionIDProducesTheSameIDAsBefore(t *testing.T) {
	env := core.Envelope{
		Provider: "open_meteo", Entity: "hourly_temperature",
		SourceKey: "-23.55|-46.63|2026-01-01T00:00", RecordTS: "2026-01-01T00:00",
	}
	esperado, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}

	got := aplica(t, IngestionID(), map[string]any{
		"provider": "open_meteo", "entity": "hourly_temperature",
		"source_key": "-23.55|-46.63|2026-01-01T00:00", "record_ts": "2026-01-01T00:00",
	})

	if got[ColumnIngestionID] != esperado {
		t.Errorf("o id mudou de fórmula:\n  transformer: %v\n  antes:       %v",
			got[ColumnIngestionID], esperado)
	}
}

// E contra o valor congelado, escrito por extenso: um teste que compara duas
// implementations passes if both change together.
func TestIngestionIDAgainstTheFrozenValue(t *testing.T) {
	got := aplica(t, IngestionID(), map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "2026-01-01T00:00:00Z",
	})

	// Checked against Python's uuid.uuid5, which is the parity that matters:
	//   uuid.uuid5(UUID("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"),
	//              "p|e|k|2026-01-01T00:00:00Z")
	const congelado = "178d0b49-dece-5738-b8eb-f5cae222a1ea"
	if got[ColumnIngestionID] != congelado {
		t.Errorf("ingestion_id = %v, congelado em %s", got[ColumnIngestionID], congelado)
	}
}

func TestIngestionIDReadsTheNamesYouGiveIt(t *testing.T) {
	comCanonicos := aplica(t, IngestionID(), map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t",
	})
	comOutros := aplica(t, IngestionID("provider", "entity", "source_key", "time"),
		map[string]any{"provider": "p", "entity": "e", "source_key": "k", "time": "t"})

	if comCanonicos[ColumnIngestionID] != comOutros[ColumnIngestionID] {
		t.Error("nomear os campos tem de dar o mesmo id que os canônicos")
	}
}

// A named-but-absent field is an error naming the field -- it usually means the
// chain is out of order, or that Without ran first.
func TestIngestionIDRefusesAMissingField(t *testing.T) {
	_, err := IngestionID()(map[string]any{"provider": "p", "entity": "e"})
	if err == nil {
		t.Fatal("faltando source_key e record_ts, o id seria construído de vazio")
	}
	for _, quer := range []string{"source_key", "record_ts"} {
		if !strings.Contains(err.Error(), quer) {
			t.Errorf("o erro não nomeia %q: %v", quer, err)
		}
	}
	// E diz o que a linha tem, para o conserto sair de uma leitura.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro não lista o que a linha tem: %v", err)
	}
}

// An empty source_key is the case Envelope.IngestionID already refused: without
// it there is no stable identity, and the id would change on every run.
func TestIngestionIDRefusesAnEmptySourceKey(t *testing.T) {
	_, err := IngestionID()(map[string]any{
		"provider": "p", "entity": "e", "source_key": "", "record_ts": "t",
	})
	if err == nil {
		t.Fatal("source_key vazio não dá identidade estável")
	}
}

func TestIngestionIDRefusesToOverwrite(t *testing.T) {
	_, err := IngestionID()(map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t",
		"ingestion_id": "meu-proprio",
	})
	if err == nil {
		t.Fatal("sobrescrever um id que a linha já tem seria invisível")
	}
}

func TestIngestionIDRefusesTheWrongNumberOfFields(t *testing.T) {
	_, err := IngestionID("provider", "entity")(map[string]any{"provider": "p", "entity": "e"})
	if err == nil {
		t.Fatal("a fórmula tem quatro componentes; dois não dá")
	}
	if !strings.Contains(err.Error(), "4") {
		t.Errorf("o erro precisa dizer quantos: %v", err)
	}
}

func TestIngestionLoadedAtWritesNowInUTC(t *testing.T) {
	got := aplica(t, IngestionLoadedAt(), map[string]any{"a": 1})

	v, ok := got[ColumnIngestionLoadedAt].(string)
	if !ok {
		t.Fatalf("ingestion_loaded_at = %T", got[ColumnIngestionLoadedAt])
	}
	quando, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatalf("não é RFC 3339: %q", v)
	}
	if d := time.Since(quando); d > time.Minute || d < -time.Minute {
		t.Errorf("ingestion_loaded_at = %v, esperado agora", quando)
	}
	if !strings.HasSuffix(v, "Z") {
		t.Errorf("esperado UTC: %q", v)
	}
	if got["a"] != 1 {
		t.Error("o resto da linha se perdeu")
	}
}

func TestIngestionLoadedAtRefusesToOverwrite(t *testing.T) {
	_, err := IngestionLoadedAt()(map[string]any{"ingestion_loaded_at": "ontem"})
	if err == nil {
		t.Fatal("sobrescrever o instante da carga seria invisível")
	}
}

// TestTransformersWriteInPlace pins the NEW contract, and it is the opposite of
// the
// que este teste afirmava antes.
//
// Cada transformer devolvia um mapa novo, "porque o chamador ainda pode estar
// holding the map". That is true once -- for the map the decoder handed over --
// and the other six copies per record were identical work repeated. The copy is
// now made once, in `applyAll`.
//
// Whoever calls a transformer ALONE, outside the chain, now sees their own row
// altered. It is documented on the Transformer type, and is the price of the
// arithmetic
// que o teste seguinte mede.
func TestTransformersWriteInPlace(t *testing.T) {
	linha := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t"}
	saida := aplica(t, IngestionID(), linha)

	if _, ok := linha[ColumnIngestionID]; !ok {
		t.Error("o transformer devolveu um mapa novo; a economia da cadeia depende de ele escrever no lugar")
	}
	if fmt.Sprint(saida) != fmt.Sprint(linha) {
		t.Error("o que voltou não é o mesmo mapa que entrou")
	}
}

// TestTransformDoesNotMutateWhatExtractDelivered is the guarantee that came to
// matter, and that did not exist as a test before.
//
// O preview do extract guarda o registro que a FONTE mandou, para mostrar
// exatamente isso. Se a cadeia escrevesse por cima dele, o preview passaria a
// show the Transform's result claiming it is the source's response -- a lie
// nobody would have any way to notice.
func TestTransformDoesNotMutateWhatExtractDelivered(t *testing.T) {
	original := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t"}

	saida, pulou, err := applyAll([]Transformer{IngestionID(), IngestionLoadedAt()}, original)
	if err != nil || pulou {
		t.Fatalf("applyAll: %v, pulou=%v", err, pulou)
	}

	if len(original) != 4 {
		t.Errorf("a cadeia escreveu no registro do extract: %v", original)
	}
	obj := saida.(map[string]any)
	if len(obj) != 6 {
		t.Errorf("a saída não tem as duas colunas novas: %v", obj)
	}
}

// TestTheChainMakesOneCopyOnly is the arithmetic that justifies the change.
func TestTheChainMakesOneCopyOnly(t *testing.T) {
	fns := []Transformer{
		Accept("provider", "entity", "source_key", "record_ts"),
		Rename(map[string]string{"record_ts": "ts"}),
		Compute("extra", func(map[string]any) (any, error) { return 1, nil }),
		Without("extra"),
	}

	alocacoes := testing.AllocsPerRun(200, func() {
		linha := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t", "lixo": 1}
		if _, _, err := applyAll(fns, linha); err != nil {
			t.Fatal(err)
		}
	})

	// The input row costs one map, the chain's copy costs another. Four
	// transformers that each copied would cost four more -- and it is that
	// difference the ceiling catches, not an absolute allocation count.
	const teto float64 = 12
	if alocacoes > teto {
		t.Errorf("%.0f alocações para uma linha e quatro transformers (teto %.0f); "+
			"algum transformer voltou a copiar o mapa", alocacoes, teto)
	}
	t.Logf("%.0f alocações", alocacoes)
}
