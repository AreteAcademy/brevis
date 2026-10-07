package core

import (
	"strings"
	"testing"
)

func cols(pairs ...any) map[string]ColumnType {
	out := map[string]ColumnType{}
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1].(ColumnType)
	}
	return out
}

// Turning flattening on does not only ADD columns. It abandons them, in two
// ways, and both are silent: nothing drops a column, so the old one stays
// full of the old rows and every row after that has NULLs in it.
//
// Refused rather than warned, for the reason the prefix change is: the wrong
// outcome has no symptom. The table looks right, the load succeeds, and the
// query somebody wrote against the old column just stops seeing new rows.
func TestCheckNormalizeRenamesRefusesBothKinds(t *testing.T) {
	if !NormalizeData() {
		t.Skip("off in this process; the child-process tests cover it on")
	}
}

// The case rename: `userName` becomes `username`, whether or not anything is
// nested.
func TestARenamedByCaseColumnIsRefused(t *testing.T) {
	err := checkNormalizeRenames(true,
		[]string{"username", "id"},
		cols("userName", TypeString, "id", TypeString),
		"bronze.orders")
	if err == nil {
		t.Fatal("accepted: `userName` stays, `username` is added, and every " +
			"row after this has a NULL in one of them")
	}
	for _, want := range []string{"userName", "username", "bronze.orders", "rename"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// The flattened-away column, which is the case the consumer is actually
// turning this on for: `name` held objects and now its fields have columns of
// their own, so nothing writes `name` any more.
func TestAFlattenedAwayColumnIsRefused(t *testing.T) {
	err := checkNormalizeRenames(true,
		[]string{"id", "name_first", "name_last"},
		cols("id", TypeString, "name", TypeJSON),
		"bronze.orders")
	if err == nil {
		t.Fatal("accepted: `name` keeps the objects already landed and " +
			"nothing writes it again")
	}
	for _, want := range []string{"name", "name_first", "bronze.orders"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// What it must NOT refuse.
func TestCheckNormalizeRenamesIsNarrow(t *testing.T) {
	for _, c := range []struct {
		name     string
		declared []string
		inTable  map[string]ColumnType
	}{
		{"off is off", []string{"username"}, cols("userName", TypeString)},

		{"an empty table — a first run", []string{"username"}, nil},

		{"nothing to rename", []string{"id", "name_first"},
			cols("id", TypeString, "name_first", TypeString)},

		// The brief's own example: `name` is an object in one record and a
		// scalar in another, so the batch declares BOTH. Nothing is
		// abandoned and refusing would break the case the feature exists for.
		{"name declared both ways", []string{"name", "name_first", "name_last"},
			cols("name", TypeJSON, "name_first", TypeString)},

		// A text column called `user` beside a producer field literally named
		// `user_name`. It never held objects, so nothing was flattened away.
		{"a text column that shares a prefix", []string{"user_name"},
			cols("user", TypeString)},

		// Already lower-case: there is no rename to make.
		{"already lower-case", []string{"username"}, cols("username", TypeString)},
	} {
		t.Run(c.name, func(t *testing.T) {
			on := c.name != "off is off"
			if err := checkNormalizeRenames(on, c.declared, c.inTable, "t"); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}
