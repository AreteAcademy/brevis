package core

import "sort"

// NilThroughout names the fields no record of the batch gave a value.
//
// It is the one place this question is answered, because three checks have to
// give the same answer about the same batch and they run in different
// packages: Discovered decides whether a column exists, CheckRow decides
// whether the row may carry it, and the SQL drivers decide whether it is an
// extra. Two of them disagreeing is a batch refused by one and written by
// another.
//
// PER BATCH and not per record. A field with a value in record 5 is a column
// the table needs, and the records that sent nil for it simply write NULL
// there -- which is the whole reason the answer cannot be read off records[0].
//
// A field absent from a record says nothing either way: absent and null land
// the same NULL, so what matters is only whether SOME record gave a value.
func NilThroughout(records []Envelope) map[string]bool {
	valued := map[string]bool{}
	nils := map[string]bool{}
	for _, e := range records {
		row, err := AsObject(e.Payload)
		if err != nil {
			// Not an object is not this function's error to report: CheckRow
			// says it, with the message it has always said it with.
			continue
		}
		for k, v := range row {
			if v == nil {
				nils[k] = true
				continue
			}
			valued[k] = true
		}
	}
	for k := range valued {
		delete(nils, k)
	}
	return nils
}

// RowFields names the columns a batch's rows contribute.
//
// The union of the keys, less the fields that are nil in every record AND that
// the table does not have. Those contribute nothing: no column was created for
// them, nothing is written, and a null and an absent field land the same NULL.
// Treating them as columns would have the destination refuse a batch over a
// value it was never going to write.
//
// A nil-throughout field the table DOES have stays, because there the column
// exists and NULL is a value for it.
func RowFields(records []Envelope, inTable []string) []string {
	have := make(map[string]bool, len(inTable))
	for _, c := range inTable {
		have[c] = true
	}
	empty := NilThroughout(records)

	seen := map[string]bool{}
	for _, e := range records {
		row, err := AsObject(e.Payload)
		if err != nil {
			continue
		}
		for k := range row {
			if empty[k] && !have[k] {
				continue
			}
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
