// Package slack is a notify.Notifier that posts to a Slack channel through an
// incoming webhook (https://api.slack.com/messaging/webhooks). One webhook
// posts to one channel, so use one notifier per audience.
package slack

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/internal/apiclient"
	"github.com/Tight-Line/heinzel/notify"
)

// Notifier posts to Slack.
type Notifier struct {
	webhook string
	api     *apiclient.Client
}

var (
	_ notify.Notifier = (*Notifier)(nil)
	_ core.Checker    = (*Notifier)(nil)
)

// New returns a notifier posting to the incoming webhook URL.
func New(webhookURL string) *Notifier {
	return &Notifier{webhook: webhookURL, api: &apiclient.Client{Name: "slack", BaseURL: webhookURL}}
}

// Notify posts the event's message.
func (n *Notifier) Notify(ctx context.Context, ev *notify.Event) error {
	return n.api.Do(ctx, http.MethodPost, "", nil, payload(ev.Message), nil)
}

// Check validates the webhook URL. Slack has no way to test a webhook
// without posting to the channel, so the URL itself is all that's checked.
func (n *Notifier) Check(context.Context) error {
	u, err := url.Parse(n.webhook)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("the webhook must be an https URL")
	}
	return nil
}

// Slack's limits: a header is at most 150 characters, a section 3000.
const (
	maxHeader  = 150
	maxSection = 3000
)

type block struct {
	Type string `json:"type"`
	Text *text  `json:"text,omitempty"`
}

type text struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func payload(msg core.Message) map[string]any {
	body := Mrkdwn(msg.Body)
	var extra []string
	for _, l := range msg.Links {
		if !strings.Contains(msg.Body, l.URL) {
			extra = append(extra, "<"+l.URL+"|"+escape(l.Title)+">")
		}
	}
	if len(extra) > 0 {
		body = strings.TrimSpace(body + "\n\n" + strings.Join(extra, " · "))
	}

	var blocks []block
	if msg.Title != "" {
		blocks = append(blocks, block{Type: "header", Text: &text{Type: "plain_text", Text: truncate(msg.Title, maxHeader)}})
	}
	for _, part := range split(body, maxSection) {
		blocks = append(blocks, block{Type: "section", Text: &text{Type: "mrkdwn", Text: part}})
	}
	// text is what notifications and clients without blocks show.
	fallback := msg.Title
	if fallback == "" {
		fallback = truncate(body, maxSection)
	}
	return map[string]any{"text": fallback, "blocks": blocks}
}

var (
	boldRE     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	linkRE     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	headingRE  = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	taskDoneRE = regexp.MustCompile(`(?m)^(\s*)[-*] \[[xX]\] `)
	taskOpenRE = regexp.MustCompile(`(?m)^(\s*)[-*] \[ \] `)
	bulletRE   = regexp.MustCompile(`(?m)^(\s*)[-*] `)
)

// Mrkdwn converts the CommonMark the engine writes into Slack's mrkdwn:
// bold, links, headings, task lists and bullets. Code and _italics_ are the
// same in both.
func Mrkdwn(md string) string {
	s := strings.TrimSpace(md)
	s = escape(s)
	s = linkRE.ReplaceAllString(s, "<$2|$1>")
	s = headingRE.ReplaceAllString(s, "*$1*")
	s = boldRE.ReplaceAllString(s, "*$1*")
	s = taskDoneRE.ReplaceAllString(s, "$1☑ ")
	s = taskOpenRE.ReplaceAllString(s, "$1☐ ")
	s = bulletRE.ReplaceAllString(s, "$1• ")
	return s
}

// escape escapes the characters Slack treats as markup.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// split cuts s into parts of at most max characters, preferring line breaks.
func split(s string, max int) []string {
	var parts []string
	for utf8.RuneCountInString(s) > max {
		r := []rune(s)
		cut := strings.LastIndex(string(r[:max]), "\n")
		if cut <= 0 {
			cut = len(string(r[:max]))
		}
		parts = append(parts, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}
