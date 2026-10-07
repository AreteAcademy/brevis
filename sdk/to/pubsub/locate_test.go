package pubsub

import "testing"

func TestTopicLocate(t *testing.T) {
	if got := (Topic{Project: "acme-prod", Name: "clicks-raw"}).Locate(); got != "pubsub://acme-prod/clicks-raw" {
		t.Fatalf("Locate() = %q", got)
	}
	// Both are required on a Topic, and a target with a hole in it would group
	// unrelated topics.
	if got := (Topic{Name: "clicks-raw"}).Locate(); got != "" {
		t.Fatalf("Locate() without a project = %q, want empty", got)
	}
}
