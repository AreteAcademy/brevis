package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chaveDeTeste(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func storePronto(t *testing.T) (FileStore, string) {
	t.Helper()
	// t.TempDir vem 0755 nesta plataforma, e o store recusa diretorio frouxo
	// -- which is the requested behaviour, so the test adjusts to it.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	return FileStore{Name: "gabriel-session"}, dir
}

// TestItStoresAndReturns: the happy path, and the only one the consumer will
// ever see.
func TestItStoresAndReturns(t *testing.T) {
	s, dir := storePronto(t)

	const valor = "session=eyJhbGciOiJkaXIi..QUJDRA=="
	if err := s.Save(valor); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != valor {
		t.Errorf("Load = %q, esperado %q", got, valor)
	}

	// And the value is not in the clear in the file.
	bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bruto), "eyJhbGciOiJkaXIi") {
		t.Error("a credencial esta em claro no arquivo")
	}
	if !strings.HasPrefix(string(bruto), formatEncrypted+"\n") {
		t.Errorf("o arquivo nao comeca com a versao: %q", bruto[:min(20, len(bruto))])
	}
}

// TestAbsentIsNotAnError: there being no stored value is a normal state -- the
// very first run. The caller falls back to the seed.
func TestAbsentIsNotAnError(t *testing.T) {
	s, _ := storePronto(t)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load de arquivo ausente devolveu erro: %v", err)
	}
	if got != "" {
		t.Errorf("Load = %q, esperado vazio", got)
	}
}

// TestTheNonceDoesNotRepeat: reusing a nonce with the same key in GCM breaks the
// cipher, and it is the most common mistake of whoever implements this for the
// first time.
func TestTheNonceDoesNotRepeat(t *testing.T) {
	s, dir := storePronto(t)

	vistos := map[string]bool{}
	for i := 0; i < 50; i++ {
		if err := s.Save("mesmo-valor-sempre"); err != nil {
			t.Fatal(err)
		}
		bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
		if err != nil {
			t.Fatal(err)
		}
		_, corpo, _ := firstLine(bruto)
		nonce := string(corpo[:12])
		if vistos[nonce] {
			t.Fatalf("nonce repetido na escrita %d", i)
		}
		vistos[nonce] = true
	}
}

// TestSemChaveGravaEmClaro: a cifra e opcional. O controle de verdade e o do
// storage's -- the directory's permissions, the bucket's IAM -- and a key living
// in the same secret as whoever reads the store protects against nobody.
func TestSemChaveGravaEmClaro(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, "")

	s := FileStore{Name: "x"}
	if err := s.CheckStore(); err != nil {
		t.Fatalf("sem chave virou erro: %v", err)
	}

	const valor = "session=abc=="
	if err := s.Save(valor); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil || got != valor {
		t.Fatalf("Load = (%q, %v)", got, err)
	}

	bruto, err := os.ReadFile(filepath.Join(dir, "x.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(bruto), formatPlaintext+"\n") {
		t.Errorf("o claro tambem precisa de versao na primeira linha: %q", bruto)
	}
}

// TestPlaintextWarnsOnlyOnce: a warning repeated on every pipeline becomes
// noise, and noise is how a warning stops being read.
func TestPlaintextWarnsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, "")
	warned.Delete(filepath.Join(dir, "x.cred"))

	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	s := FileStore{Name: "x"}
	for i := 0; i < 5; i++ {
		if err := s.CheckStore(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "writing in the clear"); n != 1 {
		t.Errorf("avisou %d vezes, esperado 1", n)
	}
}

// TestComChaveNaoGravaEmClaro: e o outro lado -- a opcao de cifrar tem de
// realmente cifrar.
func TestComChaveNaoGravaEmClaro(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("session=abc=="); err != nil {
		t.Fatal(err)
	}
	bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bruto), "session=abc==") {
		t.Error("com chave, gravou em claro")
	}
	if !strings.HasPrefix(string(bruto), formatEncrypted+"\n") {
		t.Errorf("cabecalho errado: %q", bruto[:min(20, len(bruto))])
	}
}

// TestCifradoSemChaveCaiNaSemente: durante um rollout, ou depois de alguem
// removing the key, the store holds a value this process cannot read. Falling
// back to the seed is right; returning garbage would be worse.
func TestCifradoSemChaveCaiNaSemente(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("segredo"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialKey, "")

	got, err := FileStore{Name: "gabriel-session"}.Load()
	if err != nil {
		t.Fatalf("virou erro: %v", err)
	}
	if got != "" {
		t.Errorf("Load = %q, esperado vazio", got)
	}
	_ = dir
}

// TestAKeyOfTheWrongLength: AES-256 wants 32 bytes, and a short key would fail
// later with a message about block size. A key that is present and bad is an
// error; an ABSENT key is a choice.
func TestAKeyOfTheWrongLength(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	for _, ruim := range []string{
		base64.StdEncoding.EncodeToString([]byte("curta")),
		"isto nao e base64!!",
	} {
		t.Setenv(EnvCredentialKey, ruim)
		if err := (FileStore{Name: "x"}).CheckStore(); err == nil {
			t.Errorf("aceitou a chave %q", ruim)
		}
	}
}

// TestWithNoDirectoryItTurnsOffInsteadOfFailing: with no Dir and no env var, the
// behaviour is exactly what it was before the feature -- and that is how it stays
// a shortcut, not a
// requisito.
func TestWithNoDirectoryItTurnsOffInsteadOfFailing(t *testing.T) {
	t.Setenv(EnvCredentialDir, "")
	t.Setenv(EnvCredentialKey, "")

	s := FileStore{Name: "x"}
	if err := s.CheckStore(); err != nil {
		t.Fatalf("store desligado virou erro: %v", err)
	}
	if v, err := s.Load(); err != nil || v != "" {
		t.Errorf("Load desligado = (%q, %v)", v, err)
	}
	if err := s.Save("qualquer"); err != nil {
		t.Errorf("Save desligado = %v", err)
	}
}

// TestALooseDirectoryIsRefused: a shared volume at 0777 is a public directory,
// and keeping a credential in it is no better than not keeping it.
func TestALooseDirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	err := FileStore{Name: "x"}.CheckStore()
	if err == nil {
		t.Fatal("diretorio 0777 foi aceito")
	}
	if !strings.Contains(err.Error(), "0700") {
		t.Errorf("o erro nao diz o que fazer: %v", err)
	}
}

// TestPermissions: the file at 0600, the directory at 0700.
func TestPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "criado-por-mim")
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	s := FileStore{Name: "x"}
	if err := s.Save("v"); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("diretorio = %04o, esperado 0700", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "x.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("arquivo = %04o, esperado 0600", got)
	}
}

// TestAnUnreadableFileFallsBackToTheSeed: a future version, truncated, or a
// swapped key. None of them is an error: failing would trade a stale credential
// for none at all, and a future version on a shared volume is a normal scenario
// during a rollout.
func TestAnUnreadableFileFallsBackToTheSeed(t *testing.T) {
	casos := map[string][]byte{
		"versao futura": []byte("brevis-cred/9\nqualquer coisa aqui dentro"),
		"sem versao":    []byte("nao tem newline nenhum"),
		"truncado":      []byte(formatEncrypted + "\ncurto"),
		"nao decifra":   append([]byte(formatEncrypted+"\n"), make([]byte, 60)...),
		"arquivo vazio": {},
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			s, dir := storePronto(t)
			if err := os.WriteFile(filepath.Join(dir, "gabriel-session.cred"), conteudo, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load()
			if err != nil {
				t.Fatalf("Load virou erro em vez de cair na semente: %v", err)
			}
			if got != "" {
				t.Errorf("Load = %q, esperado vazio", got)
			}
		})
	}
}

// TestASwappedKeyDoesNotDecrypt: it is the test proving the cipher is the key,
// not
// so um encode.
func TestASwappedKeyDoesNotDecrypt(t *testing.T) {
	// t.TempDir vem 0755 nesta plataforma, e o store recusa diretorio frouxo
	// -- que e o comportamento pedido, entao o teste se ajusta a ele.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)

	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	if err := (FileStore{Name: "x"}).Save("segredo"); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	got, err := FileStore{Name: "x"}.Load()
	if err != nil {
		t.Fatalf("chave trocada virou erro: %v", err)
	}
	if got != "" {
		t.Errorf("decifrou com outra chave: %q", got)
	}
}

// TestAdulteracaoEDetectada: GCM autentica. Um byte trocado no texto cifrado
// has to invalidate, and not produce garbage that becomes a credential.
func TestAdulteracaoEDetectada(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("segredo"); err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(dir, "gabriel-session.cred")
	bruto, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	bruto[len(bruto)-1] ^= 0xff
	if err := os.WriteFile(caminho, bruto, 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := s.Load(); err != nil || got != "" {
		t.Errorf("adulteracao passou: (%q, %v)", got, err)
	}
}

// TestANameThatIsAPathIsRefused: the name comes from the caller and never from
// the URL, and it must not escape the directory either.
func TestANameThatIsAPathIsRefused(t *testing.T) {
	t.Setenv(EnvCredentialDir, t.TempDir())
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	for _, ruim := range []string{"", "../fora", "sub/dir", ".", ".."} {
		if err := (FileStore{Name: ruim}).CheckStore(); err == nil {
			t.Errorf("aceitou Name = %q", ruim)
		}
	}
}

// TestEscritasConcorrentesNaoCorrompem: ultimo a escrever vence, e e escolha
// documented -- but the file has to stay readable, never half-written.
func TestEscritasConcorrentesNaoCorrompem(t *testing.T) {
	s, _ := storePronto(t)

	pronto := make(chan struct{})
	fim := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { fim <- struct{}{} }()
			<-pronto
			for j := 0; j < 30; j++ {
				if err := s.Save(strings.Repeat("v", i+1)); err != nil {
					t.Errorf("Save: %v", err)
					return
				}
				got, err := s.Load()
				if err != nil {
					t.Errorf("Load: %v", err)
					return
				}
				// Any of the eight values will do; what must not happen is
				// garbage.
				if got != "" && strings.Trim(got, "v") != "" {
					t.Errorf("Load devolveu conteudo corrompido: %q", got)
					return
				}
			}
		}(i)
	}
	close(pronto)
	for i := 0; i < 8; i++ {
		<-fim
	}
}

// TestTheStoreComesBeforeTheSeed: it is the order that makes the feature worth
// anything. A stored value is the result of the last rotation; the seed is what
// somebody pasted in once, and it may have expired already.
func TestTheStoreComesBeforeTheSeed(t *testing.T) {
	s, _ := storePronto(t)
	if err := s.Save("do-store"); err != nil {
		t.Fatal(err)
	}

	var sementeUsada bool
	c := &Credential{
		Value:   func(context.Context) (string, error) { sementeUsada = true; return "da-semente", nil },
		Apply:   AsCookie,
		Refresh: &Refresh{URL: "http://x", Store: s},
	}

	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "do-store" {
		t.Errorf("Get = %q, esperado o valor do store", got)
	}
	if sementeUsada {
		t.Error("a semente foi chamada mesmo havendo valor guardado")
	}
}

// TestWithNoStoredValueItUsesTheSeed: the very first run.
func TestWithNoStoredValueItUsesTheSeed(t *testing.T) {
	s, _ := storePronto(t)

	c := &Credential{
		Value:   func(context.Context) (string, error) { return "da-semente", nil },
		Apply:   AsCookie,
		Refresh: &Refresh{URL: "http://x", Store: s},
	}
	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "da-semente" {
		t.Errorf("Get = %q, esperado a semente", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
