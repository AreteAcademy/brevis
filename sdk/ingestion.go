package sdk

import (
	"github.com/google/uuid"

	"fmt"
	"strings"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// The two columns these transformers write. Exported so a fetcher can name
// them in Target.Columns without spelling the strings again.
const (
	ColumnIngestionID       = core.MetadataID
	ColumnIngestionLoadedAt = core.MetadataLoadedAt
)

// defaultIDFields are the four the frozen formula takes, in order.
var defaultIDFields = []string{"provider", "entity", "source_key", "record_ts"}

// IngestionID writes the ingestion_id column.
//
// As quatro colunas de proveniencia precisam existir antes dele na cadeia.
// See ExampleIngestionID; Compute does not take a KeySelector directly, so the
// key goes inside a function.
//
// The id is a deterministic UUID v5 over provider|entity|source_key|record_ts,
// so the same record always gets the same id and a re-run is safe. The formula,
// the namespace and the separator are frozen: a row written here has to match
// the row a Python fetcher writes for the same record.
//
// That is why this is an SDK transformer and not something a fetcher writes.
// A fmt.Sprintf in the fetcher would look identical and give a different id on
// the first float formatted differently -- and every load before it would stop
// matching.
//
// The four components are read from the record. With no arguments it reads the
// canonical names above; name them when yours differ:
//
//	sdk.IngestionID("provider", "entity", "source_key", "time")
//
// A named field the record does not have is an error naming the field, the way
// Accept is. It usually means the chain is out of order, or that Without ran
// first.
func IngestionID(fields ...string) Transformer {
	return ingestionIDCom(core.NamespacePadrao,
		func(v any) (string, error) { return asText(v), nil }, fields...)
}

// IngestionIDWith e IngestionID com a renderizacao injetada.
//
// Use it when the id has to match one from a system that has already written rows:
//
//	sdk.IngestionIDWith(pycompat.Text)
//
// The default does NOT use this, and the reason is not preference: changing the
// rendering would change the ingestion_id of every row Go has already written. A
// fetcher in production would start writing new ids for the same readings, and
// the result is the whole table duplicated on the next merge. The choice is per
// fetcher.
func IngestionIDWith(render Renderer, fields ...string) Transformer {
	return ingestionIDCom(core.NamespacePadrao, render, fields...)
}

// Namespace chooses the UUID namespace the identity is computed in.
//
//	sdk.Namespace(myNamespace).IngestionID()
//
// Different namespaces produce different ids for the SAME record, and that is
// what they are for: two pipelines reading the same source and writing to
// different tables must not collide, and two writing to the SAME table need the
// same one.
//
// The default exists and continues to exist because whoever has already written
// rows with it cannot have their ids rewritten. A NEW pipeline should choose its
// own: one namespace per landing table is what stops two different sources
// producing the same id by a coincidence of keys.
//
//	// once, in the fetcher, and never again
//	var myNamespace = uuid.MustParse("...")
//
// Changing the namespace of a pipeline that has already written rewrites every
// one of its ids. There is no cheap migration: the next run writes everything
// again, and the bronze merge duplicates the table.
func Namespace(ns uuid.UUID) Identity { return Identity{ns: ns} }

// Identity compoe os transformers de identidade num namespace escolhido.
// Ver Namespace.
type Identity struct{ ns uuid.UUID }

// IngestionID e sdk.IngestionID no namespace escolhido.
func (i Identity) IngestionID(fields ...string) Transformer {
	return i.IngestionIDWith(func(v any) (string, error) { return asText(v), nil }, fields...)
}

// IngestionIDWith e sdk.IngestionIDWith no namespace escolhido.
func (i Identity) IngestionIDWith(render Renderer, fields ...string) Transformer {
	return ingestionIDCom(i.ns, render, fields...)
}

func ingestionIDCom(ns uuid.UUID, render Renderer, fields ...string) Transformer {
	names := defaultIDFields
	if len(fields) > 0 {
		names = fields
	}

	return func(payload any) (any, error) {
		if len(names) != len(defaultIDFields) {
			return nil, fmt.Errorf("IngestionID takes %d fields (provider, entity, "+
				"source_key, record_ts) or none; got %d", len(defaultIDFields), len(names))
		}

		obj, ok := payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("IngestionID needs a JSON object, got %T", payload)
		}
		if _, taken := obj[ColumnIngestionID]; taken {
			return nil, fmt.Errorf("the record already has %q; IngestionID would overwrite it",
				ColumnIngestionID)
		}

		// A stack array: there are always four, and a heap slice per record on a
		// load of millions is identical work repeated.
		var parts [4]string
		var missing []string
		for i, name := range names {
			v, present := obj[name]
			if !present {
				missing = append(missing, name)
				continue
			}
			texto, err := render(v)
			if err != nil {
				return nil, fmt.Errorf("IngestionID, field %q: %w", name, err)
			}
			parts[i] = texto
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("IngestionID reads %s, which this record does not have. "+
				"Compute them first, or name the fields you use: IngestionID(\"provider\", "+
				"\"entity\", \"source_key\", \"time\"). The record has: %s",
				strings.Join(missing, ", "), availableKeys(obj))
		}
		if parts[2] == "" {
			return nil, fmt.Errorf("IngestionID needs %q to be set: without it there is no "+
				"stable identity, and the id would change on every run", names[2])
		}

		id, err := core.ComputeIngestionIDNo(ns, parts[0], parts[1], parts[2], parts[3])
		if err != nil {
			return nil, err
		}

		obj[ColumnIngestionID] = id
		return obj, nil
	}
}

// IngestionLoadedAt writes the ingestion_loaded_at column: when the row was
// written, RFC 3339 in UTC.
//
// Takes no arguments, and does not take an instant from the caller. A value
// from outside would turn "when this row was written" into something else with
// the same name -- and the partitioning the destination sets up assumes the
// first meaning.
func IngestionLoadedAt() Transformer {
	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("IngestionLoadedAt needs a JSON object, got %T", payload)
		}
		if _, taken := obj[ColumnIngestionLoadedAt]; taken {
			return nil, fmt.Errorf("the record already has %q; IngestionLoadedAt would overwrite it",
				ColumnIngestionLoadedAt)
		}
		obj[ColumnIngestionLoadedAt] = time.Now().UTC().Format(time.RFC3339)
		return obj, nil
	}
}
