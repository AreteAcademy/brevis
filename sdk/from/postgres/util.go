package postgres

import (
	"strings"
)

// firstLine summarizes the SQL for the log, without dumping a 40-line query
// onto every summary line.
func firstLine(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

// redact takes the DSN out of an error. pgx sometimes echoes the connection
// string, and it carries a password -- which would reach the log, the Result
// and anywhere else that shows the error.
func redact(err error, dsn string) error {
	if err == nil || dsn == "" {
		return err
	}
	if !strings.Contains(err.Error(), dsn) {
		return err
	}
	return errString(strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

type errString string

func (e errString) Error() string { return string(e) }
