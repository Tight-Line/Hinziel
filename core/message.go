package core

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
