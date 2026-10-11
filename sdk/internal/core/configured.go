package core

// Configured is a driver that says how it is set up, for the console's /data:
// the settings that decide what it reads or writes, in the order somebody
// would read them.
//
// AN ALLOWLIST, WRITTEN BY HAND. The driver lists what may be shown; nothing
// is read off its fields by reflection, because the same structs hold a DSN
// and a password and a reflected dump would publish them to every console
// login. Host, database, table, write mode: yes. DSN, password, token, a
// query string's values: never. The engine refuses a description carrying a
// credential (catalog.FindSecret), but that is the second wall, not this one.
//
// Optional, like Locator: a driver without it is described by its type alone.
type Configured interface {
	Configuration() []Setting
}

// Setting is one of a driver's settings. Value is a string, a number, a bool
// or a list of them; the key is snake_case, as the YAML of a gateway spells
// its own.
type Setting struct {
	Key   string
	Value any
}
