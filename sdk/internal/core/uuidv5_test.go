package core

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

// TestUUIDv5AgreesWithThePackage is the net that allows reimplementing the
// formula
// congelada.
//
// A one-bit divergence here would change every ingestion_id already written -- a
// previous load would stop matching a new one, and nobody would notice until the
// duplicates showed up. So the claim is not "mine is right": it is "mine
// is identical to the uuid package's", over inputs nobody hand-picked.
func TestUUIDv5AgreesWithThePackage(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	for i := 0; i < 5000; i++ {
		data := make([]byte, r.Intn(400))
		for j := range data {
			data[j] = byte(r.Intn(256))
		}

		var espaco uuid.UUID
		for j := range espaco {
			espaco[j] = byte(r.Intn(256))
		}

		quero := uuid.NewSHA1(espaco, data)
		got := uuidV5(espaco, data)
		if got != quero {
			t.Fatalf("divergiu em %d bytes de dados:\n  meu  %s\n  uuid %s", len(data), got, quero)
		}
	}
}

// TestUUIDv5InTheRealNamespace covers the case production uses, including the
// an empty key and one much larger than the stack buffer.
func TestUUIDv5InTheRealNamespace(t *testing.T) {
	entries := [][]byte{
		nil,
		[]byte(""),
		[]byte("open_meteo|hourly|123|2026-09-05T12:00:00Z"),
		make([]byte, 1000),
	}
	for _, data := range entries {
		if got, quero := uuidV5(DefaultNamespace, data), uuid.NewSHA1(DefaultNamespace, data); got != quero {
			t.Errorf("%d bytes: meu %s, uuid %s", len(data), got, quero)
		}
	}
}

// TestFormatUUIDAgreesWithString: the canonical format is a contract too --
// it goes into the column.
func TestFormatUUIDAgreesWithString(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		var u uuid.UUID
		for j := range u {
			u[j] = byte(r.Intn(256))
		}
		if got, quero := formatarUUID(u), u.String(); got != quero {
			t.Fatalf("meu %q, String() %q", got, quero)
		}
	}
}

// TestAKeyLargerThanTheStackBuffer: a 192-byte key fits on the stack, and a
// larger one falls to the heap -- both have to produce the same id.
func TestAKeyLargerThanTheStackBuffer(t *testing.T) {
	longo := ""
	for len(longo) < 300 {
		longo += "provedor-com-nome-comprido-"
	}

	got, err := ComputeIngestionID(longo, "entidade", "chave", "2026-09-05T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	quero := uuid.NewSHA1(DefaultNamespace,
		[]byte(longo+"|entidade|chave|2026-09-05T12:00:00Z")).String()
	if got != quero {
		t.Errorf("chave longa divergiu:\n  meu  %s\n  uuid %s", got, quero)
	}
}
