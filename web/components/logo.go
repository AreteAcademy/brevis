package components

import (
	"embed"
	"slices"
	"strings"
	"unicode"
)

// The marks of the things Brevis connects to and the languages a step runs
// in: Devicon's coloured originals (MIT, LICENSE-devicon) for the languages
// and the two databases they have, Simple Icons' one-colour marks (CC0,
// LICENSE-simple-icons) for the rest, and Brevis's own breve for brevis-sql.
//
// EMBEDDED, NOT FETCHED. The console runs where there is no internet, so each
// mark is a file in logos/ compiled into the binary and written inline -- an
// inline SVG also takes the theme's colours, which an <img> would not.
//
// The files came from the UI 2.0 prototype's logos.js, converted once; a mark
// added later is a file here and a line below. logo_test.go holds that the
// files are only drawings, with no id repeated between two of them.
//
//go:embed logos/*.svg
var logoFiles embed.FS

// logoPaint is how a mark is coloured.
type logoPaint int

const (
	paintMono  logoPaint = iota // one colour, the brand's, written in the file
	paintMulti                  // the mark's own colours
	paintDark                   // a brand too dark for a dark ground: drawn in ink
	paintLight                  // a brand too light for a light ground: darkened there
	paintBrand                  // Brevis's own mark, in the accent
	paintOnWhite                // its own colours, too dark for any dark tile: on white in both themes
)

func (p logoPaint) class() string {
	return [...]string{"cg-one", "cg-multi", "cg-dark", "cg-light", "cg-brand text-accent-strong", "cg-multi cg-on-white"}[p]
}

// logo is one registered key: whose mark it is, how it is painted, and, for
// a key with no mark, the monogram drawn instead.
type logo struct {
	title    string
	paint    logoPaint
	monogram string
}

var logos = map[string]logo{
	"airflow":    {title: "Apache Airflow", paint: paintMono},
	"bigquery":   {title: "Google BigQuery", paint: paintMono},
	"brevissql":  {title: "brevis-sql", paint: paintBrand},
	"clickhouse": {title: "ClickHouse", paint: paintLight},
	"dbt":        {title: "dbt", paint: paintMono},
	"docker":     {title: "Docker", paint: paintMono},
	"duckdb":     {title: "DuckDB", paint: paintLight},
	"gcs":        {title: "Google Cloud Storage", paint: paintLight},
	"geocoding":  {title: "Google Maps", paint: paintMono},
	"go":         {title: "Go", paint: paintMulti},
	"kafka":      {title: "Apache Kafka", paint: paintDark},
	"kubernetes": {title: "Kubernetes", paint: paintMono},
	"mysql":      {title: "MySQL", paint: paintMono},
	"node":       {title: "Node.js", paint: paintMulti},
	"pandas":     {title: "pandas", paint: paintOnWhite},
	"polars":     {title: "Polars", paint: paintMono},
	"postgres":   {title: "PostgreSQL", paint: paintMulti},
	"pubsub":     {title: "Google Pub/Sub", paint: paintLight},
	"python":     {title: "Python", paint: paintMulti},
	"redis":      {title: "Redis", paint: paintMulti},
	"redshift":   {title: "Amazon Redshift", paint: paintMono},
	"s3":         {title: "Amazon S3", paint: paintMono},
	"shell":      {title: "GNU Bash", paint: paintMono},
	"snowflake":  {title: "Snowflake", paint: paintMono},
	"spark":      {title: "Apache Spark", paint: paintMono},
	"sqs":        {title: "Amazon SQS", paint: paintMono},
	"terraform":  {title: "Terraform", paint: paintMono},
	"vision":     {title: "Google Cloud", paint: paintMono},

	// No mark: the handoff's monograms.
	"file":    {title: "Local files", monogram: "FS"},
	"http":    {title: "HTTP", monogram: "{}"},
	"gateway": {title: "Brevis gateway", monogram: "→"},
	"kinesis": {title: "Amazon Kinesis", monogram: "KS"},
	"sftp":    {title: "SFTP", monogram: "SF"},
}

// logoSVG is each mark's file, read once.
var logoSVG = func() map[string]string {
	out := map[string]string{}
	for key := range logos {
		if raw, err := logoFiles.ReadFile("logos/" + key + ".svg"); err == nil {
			out[key] = strings.TrimSpace(string(raw))
		}
	}
	return out
}()

// LogoKeys is every registered key, for the gallery.
func LogoKeys() []string {
	keys := make([]string, 0, len(logos))
	for k := range logos {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// lookLogo is a key's entry; one nobody registered is its own monogram, the
// first two letters of its name, so an unknown connector is still told apart
// from its neighbours and never drawn as somebody else's mark.
func lookLogo(key string) logo {
	if l, ok := logos[key]; ok {
		return l
	}
	letters := []rune{}
	for _, r := range key {
		if unicode.IsLetter(r) {
			letters = append(letters, unicode.ToUpper(r))
		}
		if len(letters) == 2 {
			break
		}
	}
	return logo{title: key, monogram: string(letters)}
}

func logoSize(size int) string {
	switch size {
	case 28:
		return "cg-28"
	case 40:
		return "cg-40"
	}
	return "cg-18"
}

func countMonograms() int {
	n := 0
	for _, l := range logos {
		if l.monogram != "" {
			n++
		}
	}
	return n
}
