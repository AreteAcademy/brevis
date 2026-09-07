package sdk_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// The three invariants of §14 of SDK_DECISIONS, as tests.
//
// They were written as "a product decision, not a technical one" -- and stayed
// open for months. An invariant that exists only in prose is an intention;
// written as a test, it is a property.

// I2 is exercised where it DECIDES, in load.CreationPlan -- a pure function,
// testable with no BigQuery project. A test here that only asserted "it errored"
// would not tell a refusal for a missing Schema from a missing credential, and a
// test that does not distinguish is nearly one that cannot fail.

// I2, the part that can be asserted without a server: a declaration with no type
// is refused, and the refusal says what to write.
func TestI2SchemaRequiresAType(t *testing.T) {
	target := sdk.Target{
		To:     postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Schema: sdk.Schema{{Name: "a"}},
	}
	err := sdk.ValidateTarget(target)
	if err == nil {
		t.Fatal("coluna sem Type passou")
	}
	for _, quero := range []string{"a", "Type", "TypeString"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}

// Columns and Schema together are two sources of truth, and the one that loses
// loses in silence.
func TestI2ColumnsAndSchemaTogetherIsRefused(t *testing.T) {
	err := sdk.ValidateTarget(sdk.Target{
		To:      postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Columns: []string{"a"},
		Schema:  sdk.Schema{{Name: "a", Type: sdk.TypeString}},
	})
	if err == nil {
		t.Fatal("declarar Columns e Schema passou")
	}
	if !strings.Contains(err.Error(), "drop Columns") {
		t.Errorf("o erro não diz qual manter: %v", err)
	}
}

// I3: the divergence shows up BEFORE the extract.
//
// The same check already ran in Load. What changed is the timing, and on a
// vendor with a quota that is the difference between one metadata query and the
// whole quota window spent to find out that a column does not match.
func TestI3ChecksBeforeTheExtract(t *testing.T) {
	var extraiu bool
	source := recordingSource{&extraiu}

	p := sdk.Pipeline{
		Name:   "i3",
		Source: sdk.Source{From: source},
		Target: sdk.Target{
			To:      refusingTarget{},
			Columns: []string{"coluna_que_nao_existe"},
		},
	}
	err := sdk.Execute(context.Background(), &p, nil)
	if err == nil {
		t.Fatal("o destino recusou e a execução seguiu")
	}
	if extraiu {
		t.Error("o extract rodou antes da conferência -- é exatamente a quota que o I3 economiza")
	}
	if !strings.Contains(err.Error(), "before the extract") {
		t.Errorf("o erro não diz que foi antes: %v", err)
	}
}

// I4: the partition is declared.
func TestI4ThePartitionIsDeclared(t *testing.T) {
	err := sdk.ValidateTarget(sdk.Target{
		To:          postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Schema:      sdk.Schema{{Name: "a", Type: sdk.TypeString}},
		PartitionBy: "nao_declarada",
	})
	if err == nil {
		t.Fatal("particionar por uma coluna que o Schema não declara passou")
	}
	if !strings.Contains(err.Error(), "nao_declarada") {
		t.Errorf("o erro não nomeia a coluna: %v", err)
	}
}

type recordingSource struct{ called *bool }

func (f recordingSource) Describe() string { return "fonte de teste" }
func (f recordingSource) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	*f.called = true
	return func(yield func(sdk.Envelope, error) bool) {}, nil
}

type refusingTarget struct{}

func (refusingTarget) Describe() string { return "destino de teste" }
func (refusingTarget) Write(context.Context, []sdk.Envelope, sdk.WriteOptions) (*sdk.LoadResult, error) {
	return nil, fmt.Errorf("não deveria chegar aqui")
}
func (refusingTarget) CheckDestination(_ context.Context, columns []string) error {
	return fmt.Errorf("the declaration lists %s, which the table does not have. "+
		"Caught before the extract, so no source quota was spent", strings.Join(columns, ", "))
}
