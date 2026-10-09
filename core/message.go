package core

import "strings"

// Message is rich text that every plugin renders in its own dialect
// (Trello markdown, Jira ADF, GitLab markdown, Slack mrkdwn, email HTML).
type Message struct {
	Title string // short, single line; may be empty
	Body  string // CommonMark
	Links []Link
}

type Link struct {
	Title string
	URL   string
}

// Markdown renders the message as CommonMark: the title in bold, the body,
// then every link the body doesn't already mention.
func (m Message) Markdown() string {
	var b strings.Builder
	if m.Title != "" {
		b.WriteString("**" + m.Title + "**\n\n")
	}
	b.WriteString(strings.TrimSpace(m.Body))
	var extra []string
	for _, l := range m.Links {
		if !strings.Contains(m.Body, l.URL) {
			extra = append(extra, "- ["+l.Title+"]("+l.URL+")")
		}
	}
	if len(extra) > 0 {
		b.WriteString("\n\n" + strings.Join(extra, "\n"))
	}
	return strings.TrimSpace(b.String())
}
