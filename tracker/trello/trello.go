// Package trello is a tracker.Tracker for Trello cards.
//
// A ticket is a card. Fetch accepts a card ID, a card shortLink or a card URL.
// The ticket's Key is the card's shortLink. Comments, attachments and
// checklists come with the ticket; checklists land in Ticket.Fields.
//
// It implements every optional capability: StageMover (moves the card to the
// list named for the stage), Linker (adds URL attachments) and
// AttachmentFetcher.
//
// # Credentials
//
//   - TRELLO_API_KEY: at https://trello.com/power-ups/admin, create a Power-Up
//     (or open an existing one) in the board's workspace, then "API key" →
//     "Generate a new API key".
//   - TRELLO_TOKEN: on the same page, follow the "Token" link next to the key
//     and allow access. It needs read and write (comments, moving cards,
//     attachments) and should not expire. It acts as the Trello member who
//     authorizes it, on every board they can see, so authorize it as a
//     dedicated member (e.g. "Hinzele") who is only on the factory's board.
//     Comments then also appear under that name.
//   - Board: the board's ID or the shortLink from its URL
//     (trello.com/b/<shortLink>/…). The lists and labels Stages name must
//     exist on it; nothing is created. `check` lists the board's own names
//     when one doesn't match.
package trello

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tight-Line/hinzele/core"
	"github.com/Tight-Line/hinzele/internal/apiclient"
	"github.com/Tight-Line/hinzele/tracker"
)

// Options configure a Trello tracker. APIKey and Token come from
// https://trello.com/power-ups/admin (API key, then "Token").
type Options struct {
	APIKey string
	Token  string
	// Board, if set, is the only board whose cards are tickets (ID or
	// shortLink). Cards on other boards are refused.
	Board string
	// Stages says how each lifecycle stage shows on a card (see Stage).
	// Unmapped stages are not reflected.
	Stages map[tracker.Stage]Stage
	// BaseURL of the API; "" = https://api.trello.com.
	BaseURL string
}

// Tracker is the Trello tracker.
type Tracker struct {
	opts Options
	api  *apiclient.Client

	mu      sync.Mutex
	boardID string             // resolved Options.Board
	lists   map[string][]list  // by board ID
	labels  map[string][]label // by board ID
}

var (
	_ tracker.Tracker           = (*Tracker)(nil)
	_ tracker.StageMover        = (*Tracker)(nil)
	_ tracker.Linker            = (*Tracker)(nil)
	_ tracker.AttachmentFetcher = (*Tracker)(nil)
	_ core.Checker              = (*Tracker)(nil)
)

// New returns a Trello tracker.
func New(opts Options) *Tracker {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.trello.com"
	}
	t := &Tracker{opts: opts, lists: map[string][]list{}, labels: map[string][]label{}}
	t.api = &apiclient.Client{Name: "trello", BaseURL: base + "/1", Auth: t.authorize}
	return t
}

// authorize sends the credentials in a header rather than the query string,
// so they don't end up in logs of URLs.
func (t *Tracker) authorize(r *http.Request) {
	r.Header.Set("Authorization", fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, t.opts.APIKey, t.opts.Token))
}

type card struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Desc        string       `json:"desc"`
	ShortLink   string       `json:"shortLink"`
	ShortURL    string       `json:"shortUrl"`
	IDBoard     string       `json:"idBoard"`
	Labels      []label      `json:"labels"`
	Attachments []attachment `json:"attachments"`
	Checklists  []checklist  `json:"checklists"`
	Actions     []action     `json:"actions"`
}

type label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
	IsUpload bool   `json:"isUpload"`
}

type checklist struct {
	Name       string      `json:"name"`
	CheckItems []checkItem `json:"checkItems"`
}

type checkItem struct {
	Name  string  `json:"name"`
	State string  `json:"state"` // "complete" or "incomplete"
	Pos   float64 `json:"pos"`
}

type action struct {
	Date          time.Time `json:"date"`
	MemberCreator struct {
		FullName string `json:"fullName"`
		Username string `json:"username"`
	} `json:"memberCreator"`
	Data struct {
		Text string `json:"text"`
	} `json:"data"`
}

type list struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cardURLRE matches https://trello.com/c/<shortLink>[/<slug>].
var cardURLRE = regexp.MustCompile(`^https?://trello\.com/c/([A-Za-z0-9]+)`)

// cardID accepts a card ID, shortLink or URL and returns what the API takes.
func cardID(id string) string {
	if m := cardURLRE.FindStringSubmatch(strings.TrimSpace(id)); m != nil {
		return m[1]
	}
	return strings.TrimSpace(id)
}

func (t *Tracker) Fetch(ctx context.Context, id string) (*tracker.Ticket, error) {
	var c card
	q := url.Values{
		"fields":                      {"name,desc,shortLink,shortUrl,idBoard,labels"},
		"attachments":                 {"true"},
		"attachment_fields":           {"id,name,url,mimeType,isUpload"},
		"checklists":                  {"all"},
		"checklist_fields":            {"name"},
		"actions":                     {"commentCard"},
		"actions_limit":               {"1000"},
		"action_fields":               {"data,date"},
		"action_memberCreator_fields": {"fullName,username"},
	}
	if err := t.api.Do(ctx, http.MethodGet, "/cards/"+url.PathEscape(cardID(id)), q, nil, &c); err != nil {
		return nil, fmt.Errorf("card %s: %w", id, err)
	}
	if t.opts.Board != "" {
		board, err := t.board(ctx)
		if err != nil {
			return nil, err
		}
		if c.IDBoard != board {
			return nil, fmt.Errorf("card %s is not on board %s", id, t.opts.Board)
		}
	}
	return c.ticket(), nil
}

func (c *card) ticket() *tracker.Ticket {
	tk := &tracker.Ticket{
		Ref:         tracker.TicketRef{ID: c.ID},
		Key:         c.ShortLink,
		Title:       c.Name,
		Description: c.Desc,
		URL:         c.ShortURL,
	}
	for _, l := range c.Labels {
		name := l.Name
		if name == "" {
			name = l.Color
		}
		tk.Labels = append(tk.Labels, name)
	}
	// The API returns comments newest first; a conversation reads oldest first.
	for i := len(c.Actions) - 1; i >= 0; i-- {
		a := c.Actions[i]
		author := a.MemberCreator.FullName
		if author == "" {
			author = a.MemberCreator.Username
		}
		tk.Comments = append(tk.Comments, tracker.Comment{Author: author, Body: a.Data.Text, At: a.Date})
	}
	for _, a := range c.Attachments {
		tk.Attachments = append(tk.Attachments, tracker.Attachment{ID: a.ID, Name: a.Name, URL: a.URL, MediaType: a.MimeType, IsFile: a.IsUpload})
	}
	for _, cl := range c.Checklists {
		if tk.Fields == nil {
			tk.Fields = map[string]string{}
		}
		items := slices.Clone(cl.CheckItems)
		slices.SortStableFunc(items, func(a, b checkItem) int { return cmp.Compare(a.Pos, b.Pos) })
		var b strings.Builder
		for _, it := range items {
			mark := " "
			if it.State == "complete" {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", mark, it.Name)
		}
		tk.Fields["Checklist: "+cl.Name] = b.String()
	}
	return tk
}

// maxComment is Trello's limit on the length of a comment.
const maxComment = 16384

func (t *Tracker) Comment(ctx context.Context, id string, msg core.Message) error {
	text := msg.Markdown()
	if len(text) > maxComment {
		const cut = "\n\n…(truncated)"
		text = text[:maxComment-len(cut)]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += cut
	}
	body := map[string]string{"text": text}
	return t.api.Do(ctx, http.MethodPost, "/cards/"+url.PathEscape(cardID(id))+"/actions/comments", nil, body, nil)
}

// AddLink attaches link.URL to the card unless it's already attached.
func (t *Tracker) AddLink(ctx context.Context, id string, link core.Link) error {
	path := "/cards/" + url.PathEscape(cardID(id)) + "/attachments"
	var existing []attachment
	if err := t.api.Do(ctx, http.MethodGet, path, url.Values{"fields": {"url"}}, nil, &existing); err != nil {
		return err
	}
	if slices.ContainsFunc(existing, func(a attachment) bool { return a.URL == link.URL }) {
		return nil
	}
	body := map[string]string{"url": link.URL, "name": link.Title}
	return t.api.Do(ctx, http.MethodPost, path, nil, body, nil)
}

// FetchAttachment downloads an attachment. Files uploaded to Trello need the
// same credentials as the API; links to other sites are fetched as they are.
func (t *Tracker) FetchAttachment(ctx context.Context, _ string, a tracker.Attachment) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	if u, err := url.Parse(a.URL); err == nil && (u.Host == "trello.com" || u.Host == "api.trello.com" || strings.HasPrefix(a.URL, t.api.BaseURL)) {
		t.authorize(req)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("attachment %s: %s", a.Name, resp.Status)
	}
	return resp.Body, nil
}

// downloadClient fetches attachments. Go drops the Authorization header when
// Trello redirects a download to its storage on another host.
var downloadClient = &http.Client{Timeout: 5 * time.Minute}

// Check verifies the credentials, the board, and that every stage's list and label exists.
func (t *Tracker) Check(ctx context.Context) error {
	var me struct {
		Username string `json:"username"`
	}
	if err := t.api.Do(ctx, http.MethodGet, "/members/me", url.Values{"fields": {"username"}}, nil, &me); err != nil {
		return err
	}
	if t.opts.Board == "" {
		if len(t.opts.Stages) > 0 {
			return errors.New("Stages need a Board, to check that their lists and labels exist")
		}
		return nil
	}
	board, err := t.board(ctx)
	if err != nil {
		return err
	}
	return t.checkStages(ctx, board)
}

// board resolves Options.Board, which may be a shortLink, to its ID.
func (t *Tracker) board(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.boardID != "" {
		return t.boardID, nil
	}
	var b struct {
		ID string `json:"id"`
	}
	if err := t.api.Do(ctx, http.MethodGet, "/boards/"+url.PathEscape(t.opts.Board), url.Values{"fields": {"id"}}, nil, &b); err != nil {
		return "", fmt.Errorf("board %s: %w", t.opts.Board, err)
	}
	t.boardID = b.ID
	return b.ID, nil
}

func (t *Tracker) boardLists(ctx context.Context, board string) ([]list, error) {
	t.mu.Lock()
	cached, ok := t.lists[board]
	t.mu.Unlock()
	if ok {
		return cached, nil
	}
	var lists []list
	q := url.Values{"fields": {"name"}, "filter": {"open"}}
	if err := t.api.Do(ctx, http.MethodGet, "/boards/"+url.PathEscape(board)+"/lists", q, nil, &lists); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.lists[board] = lists
	t.mu.Unlock()
	return lists, nil
}
