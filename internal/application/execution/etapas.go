package execution

import (
	"encoding/json"
	"sort"
	"strings"
)

// marcaDoSDK is the prefix the SDK uses to speak to the engine through stdout.
//
// The pipe already existed: the executor follows the pod's log while the
// container lives, and this loop sees every line, one by one. Recognising it
// here -- and not in the executor -- makes the LOCAL executor get the same for
// free, because this code does not know which of them produced the event.
const marcaDoSDK = "@brevis:"

// tetoDeEtapas caps how many transitions one step may record.
//
// The log stream becomes a database write here. Without a cap, a pipeline in a
// loop would take Postgres down through the log path -- and the log is precisely
// what must not stop working when something is wrong. The SDK caps itself; this
// is the cap of somebody who does not trust what came down the pipe.
const tetoDeEtapas = 60

// Etapa is one phase of an SDK step, as it stands now.
//
// Indice is what identifies it, and not Nome: a pipeline with two Map stages
// announces `map` twice, and keying by name would make the second overwrite the
// first -- three declared stages collapsing into two boxes on the screen, with
// no warning.
//
// EtapaGravada is the same type under the name it travels through the database
// with: it is what a test outside this package needs to check what was
// recorded.
type Etapa struct {
	Indice  int            `json:"indice"`
	Nome    string         `json:"nome"`
	State   string         `json:"estado"`
	Ms      *int64         `json:"ms,omitempty"`
	Em      string         `json:"em"`
	Numeros map[string]any `json:"numeros,omitempty"`
}

// etapasConhecidas is a closed list on purpose: a phase this engine does not
// know is ignored, rather than becoming a meaningless box on the screen.
// EtapaGravada is the shape an Etapa takes in the JSONB column.
type EtapaGravada = Etapa

// etapasConhecidas is a closed list on purpose: a phase this engine does not
// know is ignored, rather than becoming a meaningless box on the screen.
//
// `map` and `aggregate` are the STAGE kinds, and there can be several of each
// in one pipeline. `transform` is the single collapsed box the SDK announced up
// to v0.50.0, kept so an older fetcher still draws something.
var etapasConhecidas = map[string]bool{
	"check": true, "extract": true, "load": true,
	"map": true, "aggregate": true,
	"transform": true,
}

// coletorDeEtapas builds the phases' state out of the marked lines.
//
// A phase is ONE entry that changes state, not two lines of history: the screen
// shows four boxes, not a diary.
type coletorDeEtapas struct {
	Versao string
	Etapas []Etapa
	vistos int
}

// linha consumes one log line. It returns true when the line was a marker -- and
// in that case it must NOT enter the step's log: whoever looks at the screen
// wants to see phases, not JSON in a console.
func (c *coletorDeEtapas) linha(msg string) bool {
	corpo, ok := strings.CutPrefix(msg, marcaDoSDK)
	if !ok {
		return false
	}

	// TWO formats, and the second is a bridge with an expiry date.
	//
	// The SDK up to v0.47.0 spoke Portuguese: {"tipo":"etapa","nome","estado",
	// "versao","em"}. From v0.48.0 on it speaks English: {"type":"stage","name",
	// "state","version","at"}. An engine that only understood the new one would
	// make an older fetcher's phases vanish from the screen -- no error, no log,
	// just the grey box back.
	//
	// The bridge goes away once no fetcher below v0.48.0 remains in
	// production.
	var ev struct {
		Tipo   string `json:"type"`
		Versao string `json:"version"`
		Nome   string `json:"name"`
		State  string `json:"state"`
		Ms     *int64 `json:"ms"`
		Em     string `json:"at"`
		Indice *int   `json:"index"`

		TipoPT   string `json:"tipo"`
		VersaoPT string `json:"versao"`
		NomePT   string `json:"nome"`
		EstadoPT string `json:"estado"`
		EmPT     string `json:"em"`
	}
	if err := json.Unmarshal([]byte(corpo), &ev); err != nil {
		// An unreadable marker goes back to being a log line: hiding it would
		// remove from the screen the only clue that something is writing
		// rubbish in the wrong place.
		return false
	}

	// The old format fills in the new fields, and the rest of the code sees only
	// one format.
	if ev.Tipo == "" {
		ev.Tipo, ev.Versao = traduzirTipo(ev.TipoPT), ev.VersaoPT
		ev.Nome, ev.State, ev.Em = ev.NomePT, ev.EstadoPT, ev.EmPT
	}

	if c.vistos >= tetoDeEtapas {
		return true // consumed, but not recorded
	}
	c.vistos++

	switch ev.Tipo {
	case "sdk":
		c.Versao = ev.Versao
		return true
	case "stage":
		if !etapasConhecidas[ev.Nome] {
			return true
		}
		// No index means a fetcher up to v0.50.0, which announced one box per
		// NAME -- there could be only one `transform`, so the name was the
		// identity. Reusing that box's position keeps those drawing exactly as
		// they did, instead of turning `running` and `done` into two boxes.
		indice := c.posicaoPara(ev.Nome)
		if ev.Indice != nil {
			indice = *ev.Indice
		}
		c.aplicar(Etapa{
			Indice: indice, Nome: ev.Nome, State: ev.State, Ms: ev.Ms, Em: ev.Em,
			Numeros: numerosDe(corpo),
		})
		return true
	}
	return true
}

// posicaoPara is the position an unindexed phase belongs at: the one it already
// occupies, or the next free one.
func (c *coletorDeEtapas) posicaoPara(nome string) int {
	maior := -1
	for _, e := range c.Etapas {
		if e.Nome == nome {
			return e.Indice
		}
		if e.Indice > maior {
			maior = e.Indice
		}
	}
	return maior + 1
}

// aplicar replaces the phase at the same POSITION, keeping the pipeline's order.
func (c *coletorDeEtapas) aplicar(e Etapa) {
	for i := range c.Etapas {
		if c.Etapas[i].Indice == e.Indice {
			c.Etapas[i] = e
			return
		}
	}
	c.Etapas = append(c.Etapas, e)
	sort.Slice(c.Etapas, func(i, j int) bool { return c.Etapas[i].Indice < c.Etapas[j].Indice })
}

// camposReservados are the ones that become the Etapa's own columns; the rest of
// the object is numbers the phase produced.
var camposReservados = map[string]bool{
	"type": true, "name": true, "state": true, "ms": true, "at": true, "version": true,
	"index": true,
	// The old format's; see coletorDeEtapas.linha.
	"tipo": true, "nome": true, "estado": true, "em": true, "versao": true,
}

func numerosDe(corpo string) map[string]any {
	var tudo map[string]any
	if err := json.Unmarshal([]byte(corpo), &tudo); err != nil {
		return nil
	}
	for k := range tudo {
		if camposReservados[k] {
			delete(tudo, k)
		}
	}
	if len(tudo) == 0 {
		return nil
	}
	return tudo
}

// traduzirTipo maps the old format's type. See coletorDeEtapas.linha.
func traduzirTipo(pt string) string {
	if pt == "etapa" {
		return "stage"
	}
	return pt // "sdk" is the same in both
}
