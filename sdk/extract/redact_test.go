package extract

import (
	"strings"
	"testing"
)

// The redacted URL appears on every extract log line and in every error message.
// A leak here is a live credential in a log aggregator plenty of people read.
func TestRedactLetsNoSecretThrough(t *testing.T) {
	casos := []string{
		"https://api.exemplo.com/v1?key=SEGREDO",
		"https://api.exemplo.com/v1?api_key=SEGREDO",
		"https://api.exemplo.com/v1?API_KEY=SEGREDO",
		"https://api.exemplo.com/v1?apiKey=SEGREDO",
		"https://api.exemplo.com/v1?apikey=SEGREDO",
		"https://api.exemplo.com/v1?access_token=SEGREDO",
		"https://api.exemplo.com/v1?Token=SEGREDO",
		"https://api.exemplo.com/v1?refresh-token=SEGREDO",
		"https://api.exemplo.com/v1?client_secret=SEGREDO",
		"https://api.exemplo.com/v1?secret=SEGREDO",
		"https://api.exemplo.com/v1?signature=SEGREDO",
		"https://api.exemplo.com/v1?sig=SEGREDO",
		"https://api.exemplo.com/v1?password=SEGREDO",
		"https://api.exemplo.com/v1?pwd=SEGREDO",
		"https://api.exemplo.com/v1?auth=SEGREDO",
		"https://api.exemplo.com/v1?X-Api-Key=SEGREDO",
		"https://api.exemplo.com/v1?sessionId=SEGREDO",
		"https://api.exemplo.com/v1?credentials=SEGREDO",
		// The worst of them all: the password in the userinfo, which url.String
		// prints
		// inteira.
		"https://usuario:SEGREDO@api.exemplo.com/v1",
		// And combined, so it does not pass by accident on a single one.
		"https://usuario:SEGREDO@api.exemplo.com/v1?token=SEGREDO&latitude=-23.5",
	}

	for _, c := range casos {
		got := redactURL(c)
		if strings.Contains(got, "SEGREDO") {
			t.Errorf("vazou:\n  %s\n  -> %s", c, got)
		}
	}
}

// Redacting too much must not erase what is useful for debugging: the URL has to
// stay recognizable.
func TestRedactPreservesWhatIsNotASecret(t *testing.T) {
	got := redactURL("https://api.open-meteo.com/v1/forecast?latitude=-23.55&longitude=-46.63&api_key=X")

	for _, quer := range []string{"api.open-meteo.com", "/v1/forecast", "latitude=-23.55", "longitude=-46.63"} {
		if !strings.Contains(got, quer) {
			t.Errorf("a URL perdeu %q, e assim não serve para depurar: %s", quer, got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("a chave não foi redigida: %s", got)
	}
}

// A user with no password is not a secret, and erasing it would remove useful
// information.
func TestRedactKeepsAUserWithNoPassword(t *testing.T) {
	got := redactURL("https://usuario@api.exemplo.com/v1")
	if !strings.Contains(got, "usuario") {
		t.Errorf("o usuário sem senha sumiu: %s", got)
	}
}

func TestRedactDoesNotPanicOnAnInvalidURL(t *testing.T) {
	if got := redactURL("://isto-nao-e-url"); got != "[invalid url]" {
		t.Errorf("redactURL(inválida) = %q", got)
	}
}

// The redaction errs on the safe side: "monkey" contains "key" and becomes ***.
// It is the price of not depending on guessing the name the vendor chose.
func TestRedactErrsOnTheSafeSide(t *testing.T) {
	if got := redactURL("https://x/y?monkey=1"); !strings.Contains(got, "REDACTED") {
		t.Errorf("esperado sobre-redigir: %s", got)
	}
}
