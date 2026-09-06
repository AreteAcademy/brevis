package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Param is a run parameter: what changes between two triggers of the SAME
// workflow without editing the file.
//
// It was the largest distance between this engine and Kestra/Leoflow. Without
// params there is no backfill (`load_full=true`) and no window reprocessing, and
// eight of the data repository's 51 flows could not even be converted — their
// command carries `{{ inputs.start_date }}` and the like.
type Param struct {
	Nome      string
	Tipo      TipoParam
	Padrao    string
	Descricao string

	// Enum restricts the accepted values. Empty = any that passes the type.
	Enum []string

	// Pattern is a regular expression the value has to match. It exists for the
	// author to WIDEN what the `string` type accepts by default — see `seguro`.
	Pattern string
}

type TipoParam string

const (
	ParamTexto   TipoParam = "string"
	ParamBool    TipoParam = "boolean"
	ParamInteiro TipoParam = "integer"
)

// caracteresSeguros is what a `string` accepts when the author declares no
// `pattern`.
//
// This is a defence against shell injection, not purism: a param's value goes
// INTO the step's command line, and whoever triggers a run is not necessarily
// whoever wrote the workflow. Without the restriction,
// `--date {{ .date }}` with `date = "; rm -rf /"` would be arbitrary execution
// on the worker.
//
// O conjunto cobre o que os params reais deste repositorio precisam — datas,
// selectors do dbt, uids, caminhos, listas separadas por virgula — e deixa de
// fora tudo que o shell interpreta: aspas, `;`, `|`, `&`, `$`, crase,
// parenteses e redirecionamentos.
var caracteresSeguros = regexp.MustCompile(`^[A-Za-z0-9_.:/=,+@\- ]*$`)

var nomeDeParam = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validar confere a declaracao do param, nao o valor.
func (p Param) Validar() error {
	if !nomeDeParam.MatchString(p.Nome) {
		return fmt.Errorf("param %q: the name has to be lowercase, start with a letter and hold only letters, digits and _", p.Nome)
	}
	switch p.Tipo {
	case ParamTexto, ParamBool, ParamInteiro:
	case "":
		return fmt.Errorf("param %q has no type (string, boolean or integer)", p.Nome)
	default:
		return fmt.Errorf("param %q: tipo %q desconhecido", p.Nome, p.Tipo)
	}
	if p.Pattern != "" {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return fmt.Errorf("param %q: the pattern is not valid: %w", p.Nome, err)
		}
	}
	// O padrao precisa ser valido pelas proprias regras: um default recusado so
	// apareceria no primeiro disparo agendado, de madrugada.
	if p.Padrao != "" {
		if err := p.Aceita(p.Padrao); err != nil {
			return fmt.Errorf("param %q: the default value is not valid: %w", p.Nome, err)
		}
	}
	return nil
}

// Aceita valida um VALOR contra a declaracao.
func (p Param) Aceita(valor string) error {
	switch p.Tipo {
	case ParamBool:
		if valor != "true" && valor != "false" {
			return fmt.Errorf("%q is not a boolean (use true or false)", valor)
		}
		return nil
	case ParamInteiro:
		if _, err := strconv.Atoi(valor); err != nil {
			return fmt.Errorf("%q is not an integer", valor)
		}
		return nil
	}

	if len(p.Enum) > 0 {
		for _, permitido := range p.Enum {
			if valor == permitido {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of the accepted values (%s)", valor, strings.Join(p.Enum, ", "))
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(valor) {
			return fmt.Errorf("%q does not match the pattern %q", valor, p.Pattern)
		}
		return nil
	}
	if !caracteresSeguros.MatchString(valor) {
		return fmt.Errorf("%q has a character the shell interprets; "+
			"declare a `pattern` on the param if the value genuinely needs it", valor)
	}
	return nil
}

// Resolver mistura os valores informados com os padroes e valida tudo.
//
// Chave desconhecida e ERRO, nao silencio: `--param lod_full=true` com typo
// rodaria o workflow com o padrao e ninguem perceberia que o backfill nao
// aconteceu.
func (w Workflow) Resolver(informados map[string]string) (map[string]string, error) {
	declarados := make(map[string]Param, len(w.Params))
	for _, p := range w.Params {
		declarados[p.Nome] = p
	}

	for nome := range informados {
		if _, existe := declarados[nome]; !existe {
			return nil, fmt.Errorf("workflow %q does not declare the param %q (declared: %s)",
				w.Slug, nome, nomesDe(w.Params))
		}
	}

	out := make(map[string]string, len(w.Params))
	for _, p := range w.Params {
		valor, informado := informados[p.Nome]
		if !informado {
			valor = p.Padrao
		}
		if err := p.Aceita(valor); err != nil {
			return nil, fmt.Errorf("param %q: %w", p.Nome, err)
		}
		out[p.Nome] = valor
	}
	return out, nil
}

func nomesDe(ps []Param) string {
	if len(ps) == 0 {
		return "nenhum"
	}
	nomes := make([]string, len(ps))
	for i, p := range ps {
		nomes[i] = p.Nome
	}
	return strings.Join(nomes, ", ")
}
