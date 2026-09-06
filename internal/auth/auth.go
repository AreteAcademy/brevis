// Package auth closes the Brevis interface behind an operator credential.
//
// It exists for a concrete reason: in dev, an anonymous
// `POST /workflows/<slug>/trigger` answered 303 and started the pipeline.
// Anyone on the internet could run a `dbt build` that MERGEs into the
// warehouse. An orchestration interface is a remote control for the warehouse
// -- leaving it open is the same as publishing the terminal.
//
// The scope is deliberately small: ONE operator credential, from the
// configuration. There is no user registry, no roles and no multi-user, because
// none of that exists in the product yet and inventing it here would be
// building the floor before the wall. What does exist has to be right: a slow
// derivation hash, a signed session, constant-time comparison.
//
// All stdlib. `crypto/pbkdf2` landed in the standard library in Go 1.24, which
// removes the need for `x/crypto` for the one piece that was missing.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PBKDF2 iterations. The number is high on purpose: the cost is paid once per
// human login, and it is exactly what makes a dictionary attack against a
// leaked hash expensive.
const iteracoes = 600_000

// tamanhoChave is SHA-256's -- there is no gain in deriving more bytes than the hash.
const tamanhoChave = 32

// ValidadeDaSessao is how long a login lasts. A working shift: short enough
// that a tab forgotten on a laptop does not become permanent access, long
// enough not to ask for a password in the middle of an investigation.
const ValidadeDaSessao = 12 * time.Hour

// NomeDoCookie is the session cookie's name.
const NomeDoCookie = "brevis_sessao"

// ---------------------------------------------------------------------------
// Password hash
// ---------------------------------------------------------------------------

// GerarHash produces the text that goes into the configuration, as
// `pbkdf2-sha256$<iterations>$<salt>$<key>`.
//
// The format carries the iteration count with it because that number will
// change: when we double the cost a few years from now, old hashes have to keep
// verifying. A format that stores only the digest forces invalidating everyone.
func GerarHash(senha string) (string, error) {
	sal := make([]byte, 16)
	if _, err := rand.Read(sal); err != nil {
		return "", err
	}
	chave, err := pbkdf2.Key(sha256.New, senha, sal, iteracoes, tamanhoChave)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iteracoes,
		base64.RawStdEncoding.EncodeToString(sal),
		base64.RawStdEncoding.EncodeToString(chave)), nil
}

// ConferirSenha compara a senha com o hash em tempo constante.
//
// Returns false -- and not an error -- for a malformed hash: the caller is on a
// login path, and the only safe answer there is "did not get in". The
// configuration error is caught at boot, by Credencial.Validar.
func ConferirSenha(hash, senha string) bool {
	partes := strings.Split(hash, "$")
	if len(partes) != 4 || partes[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(partes[1])
	if err != nil || iter <= 0 {
		return false
	}
	sal, err := base64.RawStdEncoding.DecodeString(partes[2])
	if err != nil {
		return false
	}
	esperado, err := base64.RawStdEncoding.DecodeString(partes[3])
	if err != nil {
		return false
	}
	obtido, err := pbkdf2.Key(sha256.New, senha, sal, iter, len(esperado))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(obtido, esperado) == 1
}

// ---------------------------------------------------------------------------
// Credencial
// ---------------------------------------------------------------------------

// Credencial e o operador unico da instalacao, vindo da configuracao.
type Credencial struct {
	Usuario string
	Hash    string

	// Segredo signs the session cookie. Changing it drops every session, which
	// is the emergency lever when a leak is suspected.
	Segredo []byte
}

// Ativa says whether a credential is configured.
func (c Credencial) Ativa() bool {
	return c.Usuario != "" && c.Hash != ""
}

// Validar refuses a half-finished configuration.
//
// Half configured is worse than nothing: whoever filled in the username
// believes they closed the door. Failing at boot is the only way that belief
// does not last until the incident.
func (c Credencial) Validar() error {
	if !c.Ativa() {
		if c.Usuario != "" || c.Hash != "" {
			return errors.New("a half-configured credential: BREVIS_AUTH_USUARIO and " +
				"BREVIS_AUTH_SENHA_HASH have to come together")
		}
		return nil
	}
	if !strings.HasPrefix(c.Hash, "pbkdf2-sha256$") {
		return errors.New("BREVIS_AUTH_SENHA_HASH is not in the expected format; " +
			"generate one with `brevis hash`")
	}
	if len(c.Segredo) < 32 {
		return errors.New("BREVIS_AUTH_SEGREDO needs at least 32 bytes " +
			"(generate one with `openssl rand -base64 48`)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessao
// ---------------------------------------------------------------------------

// emitir monta o valor assinado do cookie: `<usuario>|<expira>|<hmac>`.
//
// The signature covers the username AND the expiry. Covering only the username
// would let the client choose its own validity; covering only the expiry would
// let it swap users. It is HMAC, and not a hash of the concatenated secret,
// because the naive construction is vulnerable to length extension.
func (c Credencial) emitir(agora time.Time) string {
	corpo := c.Usuario + "|" + strconv.FormatInt(agora.Add(ValidadeDaSessao).Unix(), 10)
	return corpo + "|" + base64.RawURLEncoding.EncodeToString(c.assinar(corpo))
}

func (c Credencial) assinar(corpo string) []byte {
	m := hmac.New(sha256.New, c.Segredo)
	m.Write([]byte(corpo))
	return m.Sum(nil)
}

// conferirSessao valida assinatura e prazo do cookie.
func (c Credencial) conferirSessao(valor string, agora time.Time) bool {
	i := strings.LastIndex(valor, "|")
	if i < 0 {
		return false
	}
	corpo, assinatura := valor[:i], valor[i+1:]

	bruta, err := base64.RawURLEncoding.DecodeString(assinatura)
	if err != nil {
		return false
	}
	// The signature is checked BEFORE the deadline, and in constant time:
	// reading a field of an unsigned cookie is already trusting it.
	if !hmac.Equal(bruta, c.assinar(corpo)) {
		return false
	}

	usuario, prazo, ok := strings.Cut(corpo, "|")
	if !ok || usuario != c.Usuario {
		// Usuario diferente do configurado: a credencial mudou desde o login.
		return false
	}
	expira, err := strconv.ParseInt(prazo, 10, 64)
	if err != nil {
		return false
	}
	return agora.Unix() < expira
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// Portao envolve um handler exigindo sessao valida.
//
// The routes that need no session are few and explicit. Kubernetes probes are
// on that list out of necessity -- a /health that asks for a password kills the
// pod.
type Portao struct {
	Cred     Credencial
	Proximo  http.Handler
	Login    http.Handler // renderiza a tela de login
	Inseguro bool         // http puro: manda o cookie sem a flag Secure
}

// livre lists what answers without a session.
func livre(caminho string) bool {
	switch caminho {
	case "/health", "/ready", "/login", "/logout":
		return true
	}
	// The assets are public by nature: the CSS, fonts and JS of the login screen
	// itself. Protecting them would break the page that asks for the
	// password.
	return strings.HasPrefix(caminho, "/assets/")
}

func (p *Portao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case !p.Cred.Ativa(), livre(r.URL.Path):
		p.Proximo.ServeHTTP(w, r)
		return
	}

	cookie, err := r.Cookie(NomeDoCookie)
	if err == nil && p.Cred.conferirSessao(cookie.Value, time.Now()) {
		p.Proximo.ServeHTTP(w, r.WithContext(EmContexto(r.Context(), p.Cred.Usuario)))
		return
	}

	// A POST without a session does not become a redirect to the login: the
	// browser would lose the body and the operator would resend blindly after
	// signing in. A 401 tells the truth about what happened.
	if r.Method != http.MethodGet {
		http.Error(w, "sessao expirada; entre novamente", http.StatusUnauthorized)
		return
	}
	destino := "/login"
	if alvo := r.URL.RequestURI(); alvo != "/" {
		destino += "?de=" + escaparDestino(alvo)
	}
	http.Redirect(w, r, destino, http.StatusSeeOther)
}

// Entrar checks the credential and writes the cookie. Returns false if it did not match.
func (p *Portao) Entrar(w http.ResponseWriter, usuario, senha string) bool {
	// As duas comparacoes correm SEMPRE, mesmo com usuario errado: sair cedo
	// faz um usuario invalido responder mais rapido que um valido, e a
	// diferenca de tempo entrega quais nomes existem.
	usuarioOK := subtle.ConstantTimeCompare([]byte(usuario), []byte(p.Cred.Usuario)) == 1
	senhaOK := ConferirSenha(p.Cred.Hash, senha)
	if !usuarioOK || !senhaOK {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:  NomeDoCookie,
		Value: p.Cred.emitir(time.Now()),
		Path:  "/",
		// HttpOnly: an XSS in the interface cannot read the session.
		HttpOnly: true,
		// Lax, not Strict: the post-login redirect is a navigation from an
		// external origin, and Strict would hide the cookie on exactly that
		// one.
		SameSite: http.SameSiteLaxMode,
		Secure:   !p.Inseguro,
		Expires:  time.Now().Add(ValidadeDaSessao),
	})
	return true
}

// Sair apaga o cookie.
func (p *Portao) Sair(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: NomeDoCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: !p.Inseguro,
		MaxAge: -1,
	})
}

// escaparDestino permite apenas caminho interno no `?de=`.
//
// Without this, `/login?de=https://malicious` would make our own login screen
// hand the authenticated operator away -- the classic open redirect.
func escaparDestino(alvo string) string {
	if !strings.HasPrefix(alvo, "/") || strings.HasPrefix(alvo, "//") {
		return "/"
	}
	return alvo
}

// Destino saneia o `?de=` na hora de redirecionar pos-login.
func Destino(bruto string) string {
	if bruto == "" {
		return "/"
	}
	return escaparDestino(bruto)
}

// ---------------------------------------------------------------------------
// Sessao no contexto
// ---------------------------------------------------------------------------

type chave struct{}

// EmContexto stores the request's operator. The layout uses it to decide
// whether to show the sign-out button -- an installation with no credential
// should not display a
// botao que nao faz nada.
func EmContexto(ctx context.Context, usuario string) context.Context {
	return context.WithValue(ctx, chave{}, usuario)
}

// De devolve o operador da requisicao, ou vazio quando nao ha sessao.
func De(ctx context.Context) string {
	u, _ := ctx.Value(chave{}).(string)
	return u
}
