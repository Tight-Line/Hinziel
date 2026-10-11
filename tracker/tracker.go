// Package tracker defines where tickets come from and where progress is
// reported back (Trello, Jira, …). Implementations live in subpackages.
package tracker

import (
	"context"
	"io"
	"time"

	"github.com/Tight-Line/hinziel/core"
)

// Tracker is where tickets come from and where progress is reported.
type Tracker interface {
	Fetch(ctx context.Context, id string) (*Ticket, error)
	Comment(ctx context.Context, id string, msg core.Message) error
}

// Optional capabilities.

type StageMover interface {
	MoveTo(ctx context.Context, id string, stage Stage) error // no-op if stage unmapped
}

type Linker interface {
	AddLink(ctx context.Context, id string, link core.Link) error // idempotent by URL
}

type AttachmentFetcher interface {
	FetchAttachment(ctx context.Context, ticketID string, a Attachment) (io.ReadCloser, error)
}

// Stage is a lifecycle point that a tracker may reflect (column, status, label).
// Each tracker maps stages to its own concepts via its config.
type Stage string

const (
	StageInProgress Stage = "in_progress"
	StageInReview   Stage = "in_review"
	StageBlocked    Stage = "blocked" // failed or needs input
)

type TicketRef struct {
	Tracker string `json:"tracker"` // plugin instance name; empty = project default
	ID      string `json:"id"`      // tracker-native id (Trello card id, Jira key)
}

type Ticket struct {
	Ref         TicketRef         `json:"ref"`
	Key         string            `json:"key"` // short human key used in names: "PROJ-123", Trello shortLink
	Title       string            `json:"title"`
	Description string            `json:"description"` // CommonMark (converted by the plugin)
	URL         string            `json:"url"`
	Labels      []string          `json:"labels,omitempty"`
	Comments    []Comment         `json:"comments,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"` // checklists, custom fields, rendered as text
}

type Comment struct {
	Author string    `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	// IsFile marks a file stored in the tracker, which only the tracker's
	// credentials can download, as opposed to a link to somewhere else.
	IsFile bool `json:"is_file,omitempty"`
	// Path is a local copy, set by the engine when it downloads the file
	// for the agent (through AttachmentFetcher).
	Path string `json:"path,omitempty"`
}
