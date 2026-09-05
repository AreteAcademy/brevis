// Package sdk_test carrega os exemplos da documentacao.
//
// Eles vivem aqui, e nao dentro de um comentario, porque um exemplo em
// comentario nao compila -- e os dois que estavam em sdk.go e pipeline.go
// passaram a documentar quatro campos de Target e dois de Source que tinham
// deixado de existir. Quem copiasse recebia codigo que nao compila.
//
// E `package sdk_test`, externo de proposito: assim o exemplo se escreve com
// os mesmos `sdk.` que um consumidor escreve, e so enxerga o que e exportado.
package sdk_test

import (
	"context"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/pycompat"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

// Um fetcher inteiro em duas chamadas: de onde vem, e para onde vai.
func Example() {
	ctx := context.Background()

	dados, err := sdk.Extract(ctx, sdk.Source{
		From: from.HTTP{
			URL: "https://api.open-meteo.com/v1/forecast?latitude=-23.5&longitude=-46.6&hourly=temperature_2m",
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
			},
		},
	})
	if err != nil {
		return
	}

	// O que se leva da origem.
	dados = sdk.Transform(dados, sdk.Accept("time", "temperature_2m"))

	// Para onde vai, e com que colunas.
	if _, err := sdk.Load(ctx, dados, sdk.Target{
		To:      to.Files{Path: "./landing/temperatura/"},
		Columns: []string{"time", "temperature_2m"},
	}); err != nil {
		return
	}
}

// Pipeline e o mesmo fetcher como um valor: Run cuida das flags, do -dry-run,
// do log e do codigo de saida.
func ExamplePipeline() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL: "https://api.exemplo.com/eventos",
				Records: func(r sdk.Response) ([]any, error) {
					var eventos []any
					return eventos, r.JSON(&eventos)
				},
			},
		},
		Transform: []sdk.Transformer{
			sdk.Without("generationtime_ms"),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		},
		Target: sdk.Target{
			To:      to.Files{Path: "./landing/eventos/"},
			Columns: []string{"id", "created_at", "ingestion_id", "ingestion_loaded_at"},
		},
	})
}

// Key monta o source_key juntando campos do payload, na ordem dada.
//
// Ele produz um sdk.KeySelector, que o Compute nao aceita direto -- por isso a
// funcao em volta.
func ExampleKey() {
	_ = sdk.Compute("source_key", func(r map[string]any) (any, error) {
		return sdk.Key("latitude", "longitude", "time")(r)
	})
}

// KeyWith e Key com a renderizacao injetada, para quando a chave precisa casar
// com a de um sistema que ja gravou linhas.
func ExampleKeyWith() {
	_ = sdk.Compute("source_key", func(r map[string]any) (any, error) {
		return sdk.KeyWith(pycompat.Texto, "provider", "id")(r)
	})
}

// Field le um campo do payload como carimbo do registro.
func ExampleField() {
	_ = sdk.Compute("record_ts", func(r map[string]any) (any, error) {
		return sdk.Field("time")(r)
	})
}

// IngestionID escreve a coluna ingestion_id a partir das quatro colunas de
// proveniencia, que precisam existir antes dele na cadeia.
func ExampleIngestionID() {
	_ = []sdk.Transformer{
		sdk.Compute("provider", func(map[string]any) (any, error) { return "open_meteo", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "hourly_temperature", nil }),
		sdk.Compute("source_key", func(r map[string]any) (any, error) {
			return sdk.Key("latitude", "longitude", "time")(r)
		}),
		sdk.Compute("record_ts", func(r map[string]any) (any, error) {
			return sdk.Field("time")(r)
		}),
		sdk.IngestionID(),
	}
}

// RejectIf recusa uma resposta 200 que traz {"error": true} no corpo.
func ExampleRejectIf() {
	_ = from.HTTP{
		URL: "https://api.exemplo.com/eventos",
		Records: func(r sdk.Response) ([]any, error) {
			if err := sdk.RejectIf("error")(r); err != nil {
				return nil, err
			}
			doc, err := r.Object()
			if err != nil {
				return nil, err
			}
			return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
		},
	}
}

// Bool serve as opcoes de tres estados, onde nil ("nao dito") tem de ser
// distinguivel de false.
func ExampleBool() {
	_ = bigquery.Table{
		Dataset:     "landing",
		Name:        "temperatura",
		CreateTable: sdk.Bool(false), // nunca, nem numa primeira execucao
	}
}

// Source e a origem: o driver em From, mais o que vale para todos eles.
func ExampleSource() {
	_ = sdk.Source{
		From:    from.HTTP{URL: "https://api.exemplo.com/v1/eventos"},
		Preview: 5,
	}
}

// Target e o destino: o driver em To, mais as colunas declaradas.
func ExampleTarget() {
	_ = sdk.Target{
		To:      bigquery.Table{Dataset: "bronze", Name: "pedidos"},
		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
	}
}
