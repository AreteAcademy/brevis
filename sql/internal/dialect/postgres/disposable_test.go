package postgres_test

import (
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// POSTGRES DOES NOT IMPLEMENT IT, and that is the assertion.
//
// Its test warehouse is a container, and what a container holds is disposable
// by construction -- `docker compose down` is the expiry. There is nothing for
// a dialect to say about it, and a method returning an empty string would be a
// statement nobody runs with a comment explaining that it does nothing.
//
// The check is here rather than nowhere because the pull is real: the next
// person to touch Disposable will see two dialects and one implementation and
// reach for symmetry. This fails if they do, and says why.
func TestPostgresIsNotDisposableAndDoesNotNeedToBe(t *testing.T) {
	if _, is := any(postgres.Dialect{}).(dialect.Disposable); is {
		t.Error("postgres implements Disposable. Its test warehouse is a container: " +
			"`docker compose down` is the expiry, and a dialect has nothing to add. " +
			"If a real reason appeared, replace this test with it rather than deleting it")
	}
}
