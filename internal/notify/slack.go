// Package notify warns when a run fails.
//
// It exists because it was the biggest gap against Kestra: the 51 flows in that
// repository each carried the SAME copied `errors: alert_slack` block -- twenty
// lines of payload repeated fifty times. Here the alert is a property of the
// INSTALLATION: configure the webhook once and every workflow starts warning,
// with the option to silence one.
//
// The webhook does not come from the workflow's YAML, on purpose. It is a
// credential: whoever has the URL posts in the channel as if they were the
// platform, and a pipeline file is no place for that.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Alert is what gets told about a failure.
type Alert struct {
	Workflow    string
	RunID       string
	Status      string
	Trigger     string
	Attempts    int
	LogicalDate *time.Time
	Err         string

	// Step is the node that failed. It arrives as a field of its own, and not
	// only embedded in the error text, because it is the first thing whoever is
	// on call looks for: "which step?" before "why?".
	Step string

	// LogExcerpt is the last few lines of that step's output, read from
	// `task_runs.log`. Without it the alert says something failed; with it the
	// alert says what failed and why, without anyone opening the screen at
	// 4am.
	LogExcerpt string

	// The workflow's tags become the message's "Domain" and "Pipeline" fields --
	// in Kestra that came from `labels`, and it is what makes an alert
	// actionable without opening the screen.
	Tags []string

	// BaseURL of the UI, for the run's direct link. Empty means no link.
	BaseURL string
}

// Notificador sends the alert. A small interface so the dispatcher knows nothing
// of Slack -- and so the test needs no network.
type Notificador interface {
	Failed(ctx context.Context, a Alert) error
}

// Slack posts to an Incoming Webhook.
type Slack struct {
	Webhook string
	Cliente *http.Client

	// Ambiente appears in the header ("prod", "dev"). Without it, a staging
	// alert at three in the morning is indistinguishable from a production
	// one.
	Ambiente string
}

func NovoSlack(webhook, ambiente string) *Slack {
	return &Slack{
		Webhook:  webhook,
		Ambiente: ambiente,
		// A short timeout: warning matters, but jamming the dispatcher waiting on
		// Slack would trade one incident for another.
		Cliente: &http.Client{Timeout: 5 * time.Second},
	}
}

// Failed posts the message.
func (s *Slack) Failed(ctx context.Context, a Alert) error {
	if s.Webhook == "" {
		return nil
	}
	corpo, err := json.Marshal(s.message(a))
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.Cliente.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		// Slack answers with plain text ("invalid_payload", "no_service"), not
		// JSON. Passing the body through is what lets one tell a revoked webhook
		// from a malformed payload without opening a browser.
		reason, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("slack respondeu %s: %s", res.Status, strings.TrimSpace(string(reason)))
	}
	return nil
}

type bloco map[string]any

func (s *Slack) message(a Alert) map[string]any {
	dominio, pipeline := s.classificar(a)

	fields := []bloco{
		field("*Domain:*\n`" + dominio + "`"),
		field("*Pipeline:*\n`" + pipeline + "`"),
		field("*Status:*\n:x: " + strings.ToUpper(a.Status)),
		field("*Trigger:*\n`" + a.Trigger + "`"),
	}
	if a.Step != "" {
		fields = append(fields, field("*Step:*\n`"+a.Step+"`"))
	}
	if a.Attempts > 0 {
		fields = append(fields, field(fmt.Sprintf("*Attempts:*\n%d", a.Attempts)))
	}
	if a.LogicalDate != nil {
		// The TIMEZONE travels with it, and that is not decoration: the same
		// event renders "01:00" on the developer's machine (UTC-3) and "04:00"
		// in the pod (UTC), because Local() is the timezone of WHOEVER FORMATS.
		// Without the marker, two people comparing the same failure at three in
		// the morning disagree about when it happened.
		//
		// It stays Local(), and not fixed UTC: whoever operates decides, by
		// setting TZ on the deployment -- and now the message says which
		// decision that was.
		fields = append(fields, field("*Logical date:*\n"+
			a.LogicalDate.Local().Format("02/01/2006 15:04 MST")))
	}

	blocos := []bloco{
		{"type": "header", "text": bloco{
			"type": "plain_text", "emoji": true,
			"text": ":rotating_light: Falha no pipeline" + s.sufixoDeAmbiente(),
		}},
		{"type": "section", "fields": fields},
	}

	if a.Err != "" {
		// The error message already carries the last lines of stderr; cutting at
		// 900 characters stays under Slack's 3000-character block limit, which
		// would otherwise make the whole message be refused rather than
		// truncated.
		blocos = append(blocos, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "```" + truncar(a.Err, 900) + "```",
		}})
	}
	// The log goes in AFTER the error and separate from it: the error is the
	// conclusion, the log is the evidence. In a single block Slack cuts both at
	// the same limit, and what usually survives is the evidence without the
	// conclusion.
	if a.LogExcerpt != "" {
		blocos = append(blocos, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "*Last lines:*\n```" + truncar(a.LogExcerpt, 900) + "```",
		}})
	}
	if a.BaseURL != "" && a.RunID != "" {
		url := strings.TrimRight(a.BaseURL, "/") + "/runs/" + a.RunID
		blocos = append(blocos, bloco{"type": "context", "elements": []bloco{
			{"type": "mrkdwn", "text": "<" + url + "|open the run> · `" + a.RunID + "`"},
		}})
	}

	return map[string]any{
		// `text` outside the blocks is what shows in the phone notification and
		// in the channel list. Without it Slack shows "This content can't be
		// displayed" in the preview.
		"text":   fmt.Sprintf(":rotating_light: %s failed", a.Workflow),
		"blocks": blocos,
	}
}

// tagsDeTecnologia are tags that name HOW a pipeline is built, not what it is
// about. They never make a good domain in an alert.
var tagsDeTecnologia = map[string]bool{
	"dbt": true, "python": true, "go": true, "sql": true, "spark": true,
}

// classificar derives domain and pipeline from the tags, with the slug as a
// fallback.
//
// The convention is the one the data repository uses: the first tag is the
// product, the rest describe the subject. So the first is skipped BY POSITION
// -- it used to be skipped by name, with the product's name written into this
// file, which made a library carry one installation's vocabulary.
//
// With no tags, the slug already says enough to keep the alert from being
// anonymous.
func (s *Slack) classificar(a Alert) (dominio, pipeline string) {
	pipeline = a.Workflow
	dominio = "-"

	candidatas := a.Tags
	if len(candidatas) > 1 {
		candidatas = candidatas[1:]
	}
	for _, t := range candidatas {
		if !tagsDeTecnologia[t] {
			dominio = t
			break
		}
	}
	if dominio == "-" {
		if partes := strings.SplitN(a.Workflow, "_", 2); len(partes) == 2 {
			dominio = partes[0]
		}
	}
	return dominio, pipeline
}

func (s *Slack) sufixoDeAmbiente() string {
	if s.Ambiente == "" || s.Ambiente == "prod" {
		return ""
	}
	return " (" + s.Ambiente + ")"
}

func field(text string) bloco {
	return bloco{"type": "mrkdwn", "text": text}
}

func truncar(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncado)"
}
