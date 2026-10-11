package engine

import (
	"os"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

func TestAttachmentsAreDownloadedForTheAgent(t *testing.T) {
	f := newFixture(t)
	f.tracker.AddFile("T-1", "a1", "../Expected reach: errors.md", "# Errors\nRATE_LIMITED: retry later\n")
	f.agentChangesAPI()

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusSucceeded {
		t.Fatalf("status %s", r.Status)
	}
	a := r.Ticket.Attachments[0]
	if !strings.HasSuffix(a.Path, "/attachments/01-Expected_reach_errors.md") || !strings.HasPrefix(a.Path, f.eng.Store().Dir(r.ID)) {
		t.Fatalf("path %q must be a safe name in the run's directory", a.Path)
	}
	if data, _ := os.ReadFile(a.Path); string(data) != "# Errors\nRATE_LIMITED: retry later\n" {
		t.Errorf("content %q", data)
	}
	if prompt := f.agent.Tasks()[0].Prompt; !strings.Contains(prompt, a.Path+" (downloaded from the ticket") {
		t.Errorf("the prompt must point at the local copy:\n%s", prompt)
	}
}

func TestAttachmentThatCantBeFetchedKeepsItsURL(t *testing.T) {
	f := newFixture(t)
	f.tracker.AddFile("T-1", "a1", "spec.md", "x")
	// A link isn't downloaded, and a file the tracker can't serve isn't fatal.
	f.tracker.AddAttachment("T-1", tracker.Attachment{ID: "gone", Name: "lost.pdf", URL: "https://tracker.test/files/gone", IsFile: true})
	f.tracker.AddAttachment("T-1", tracker.Attachment{ID: "l1", Name: "Design", URL: "https://figma.test/file"})
	f.agentChangesAPI()

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	prompt := f.agent.Tasks()[0].Prompt
	for _, want := range []string{"- lost.pdf: https://tracker.test/files/gone\n", "- Design: https://figma.test/file\n", "01-spec.md (downloaded"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt must contain %q:\n%s", want, prompt)
		}
	}
	if r.Ticket.Attachments[1].Path != "" || r.Ticket.Attachments[2].Path != "" {
		t.Errorf("only fetched files get a path: %+v", r.Ticket.Attachments)
	}
}
