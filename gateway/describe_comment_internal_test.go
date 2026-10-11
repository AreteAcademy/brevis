package gateway

import "testing"

// Which comment lines are settings, and which are prose that happens to start
// with a word and a colon. Every "prose" case is a real line from the example
// configs, which the first version of this rule dropped.
func TestACommentLineIsASettingOnlyWhenShapedLikeOne(t *testing.T) {
	settings := []string{
		"password: hunter2",
		"dsn: postgres://loader:hunter2@db.internal/shop",
		"old: app:hunter2@tcp(mysql:3306)/events",
		"- name: clicks",
		"flush: {every: 60s, records: 1}",
		"allow: [app_, svc_]",
		`token: "abc123"`,
		"PG_PASSWORD=hunter2",
		"write: append   # was merge",
	}
	prose := []string{
		"on: send the same body again, the ingestion_id is a frozen",
		"_dead_letter_at: whoever finds this file has the events and not the log,",
		"for: one stream feeds whoever subscribes, another lands what somebody will",
		"string: a DSN carries a password and this file is in git.",
		"The table must exist: this does not create one.",
		"append  COPY FROM STDIN, Postgres's fast path. Every delivery lands,",
	}
	for _, l := range settings {
		if !commentedSetting.MatchString(l) {
			t.Errorf("kept as prose, but it is a setting: %q", l)
		}
	}
	for _, l := range prose {
		if commentedSetting.MatchString(l) {
			t.Errorf("dropped as a setting, but it is prose: %q", l)
		}
	}
}
