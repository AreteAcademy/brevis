// Um fetcher DE VERDADE, com o SDK de verdade, para o teste de ponta a ponta.
//
// Ele existe porque tudo o mais no caminho das etapas era testado com um
// executor falso: a linha `@brevis:` nunca tinha atravessado um pipe do sistema
// operacional, um bufio.Scanner e o laço de eventos do runner. Este binário faz
// ela atravessar.
package main

import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Name:   "fetcher-de-teste",
		Source: sdk.Source{From: from.Files{Path: "entrada.ndjson", Format: sdk.FormatNDJSON}},
		Transform: []sdk.Transformer{
			sdk.Compute("provider", func(map[string]any) (any, error) { return "teste", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "linhas", nil }),
			sdk.ComputeText("source_key", sdk.Key("id")),
			sdk.ComputeText("record_ts", sdk.Field("ts")),
			sdk.IngestionID(),
		},
		Target: sdk.Target{To: to.Files{Path: "saida/"}},
	})
}
