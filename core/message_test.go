package core

import "testing"

func TestMarkdown(t *testing.T) {
	m := Message{
		Title: "Ready",
		Body:  "See https://mr.test/1.\n",
		Links: []Link{{Title: "MR", URL: "https://mr.test/1"}, {Title: "Preview", URL: "https://preview.test"}},
	}
	want := "**Ready**\n\nSee https://mr.test/1.\n\n- [Preview](https://preview.test)"
	if got := m.Markdown(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := (Message{Body: "plain"}).Markdown(); got != "plain" {
		t.Errorf("body only: %q", got)
	}
}
