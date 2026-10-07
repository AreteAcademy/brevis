package bigquery

import (
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// This sink declares with Schema and NEVER with Columns, and that is the fact
// three bugs have depended on.
//
// Nothing asserted it until #42's third round, when the encoder read an empty
// Columns as "nothing was declared" and sent every key to BigQuery -- 4,500
// events, 8 batches, all dead-lettered, on a path that creates the table from
// the Schema sitting right beside it.
//
// Setting Columns here is the fix that looks tidy and is not: Columns means
// "every row has exactly these" and core.CheckRow enforces it against the
// FIRST record, while this Schema is the union of a batch whose records need
// not agree. An ordinary batch would be refused for a column only its second
// record carries.
func TestTheSinkDeclaresWithSchemaAndNotColumns(t *testing.T) {
	s := &sink{target: &gateway.Target{
		Schema: sdk.Schema{
			{Name: "zarv_metadata_ingestion_id", Type: sdk.TypeString},
			{Name: "id", Type: sdk.TypeString},
		},
		DedupKey:    "zarv_metadata_ingestion_id",
		PartitionBy: "zarv_metadata_received_at",
	}}

	opt := s.options()
	if len(opt.Columns) != 0 {
		t.Errorf("Columns is %v. It means `every row has exactly these`, and "+
			"CheckRow enforces it against the FIRST record -- but this Schema "+
			"is the UNION of the batch, so a record that does not carry one "+
			"of its columns would have the whole batch refused.\n\n"+
			"If this is a deliberate change, the SDK side has to stop reading "+
			"Columns as `the consumer promised this` first", opt.Columns)
	}
	if len(opt.Schema) != 2 {
		t.Errorf("Schema is %v, and it is what the table is made of", opt.Schema.Names())
	}
	if opt.DedupKey == "" || opt.PartitionBy == "" {
		t.Error("the target's dedup key and partition column did not travel")
	}
}

// And with no target there is nothing to declare: the dedup setting still
// travels, because it is the sink's own and not the table's.
func TestWithNoTargetOnlyTheDedupTravels(t *testing.T) {
	s := &sink{dedup: sdk.DedupMerge}
	opt := s.options()
	if opt.Dedup != sdk.DedupMerge {
		t.Errorf("Dedup is %v, want merge", opt.Dedup)
	}
	if len(opt.Schema) != 0 || len(opt.Columns) != 0 {
		t.Errorf("something was declared with no target: %v / %v",
			opt.Schema.Names(), opt.Columns)
	}
}
