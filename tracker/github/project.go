package github

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Tight-Line/hinziel/tracker"
)

// Project is a GitHub project (Projects, the boards that span repositories)
// whose single-select field, usually Status, follows the issue's stage: the
// board's columns. An issue that isn't on the project yet is added to it the
// first time it moves.
//
// The token needs the project scope (a classic token) or Projects read and
// write (a fine-grained one), and its user must be allowed to write to the
// project: for a user's project, a collaborator with Write.
type Project struct {
	Owner  string // the user or organization the project belongs to
	Number int    // from its URL: …/projects/<number>
	// Field is the single-select field the columns come from; "" = "Status".
	Field string
	// Columns names the option each stage sets, matched leniently (case and
	// spacing don't matter). Unmapped stages leave the field as it is.
	Columns map[tracker.Stage]string
}

func (p *Project) field() string {
	if p.Field == "" {
		return "Status"
	}
	return p.Field
}

// board is what the project's IDs resolve to.
type board struct {
	project string            // the project's node ID
	field   string            // the field's node ID
	options map[string]string // option name → option ID
}

type gqlError struct {
	Message string `json:"message"`
}

// graphql runs one query or mutation and decodes its data into out.
func (t *Tracker) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   any        `json:"data"`
		Errors []gqlError `json:"errors"`
	}
	resp.Data = out
	if err := t.gql.Do(ctx, http.MethodPost, "/graphql", nil, map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		var msgs []string
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return nil
}

const boardQuery = `query($owner: String!, $number: Int!, $field: String!) {
  repositoryOwner(login: $owner) {
    ... on User { projectV2(number: $number) { ...board } }
    ... on Organization { projectV2(number: $number) { ...board } }
  }
}
fragment board on ProjectV2 {
  id
  field(name: $field) { ... on ProjectV2SingleSelectField { id options { id name } } }
}`

// board looks up the project, its field and the field's options, once:
// a failed lookup is tried again next time, as the token or the project may
// have been fixed by then.
func (t *Tracker) board(ctx context.Context) (*board, error) {
	t.boardMu.Lock()
	defer t.boardMu.Unlock()
	if t.boardIDs != nil {
		return t.boardIDs, nil
	}
	p := t.opts.Project
	var out struct {
		RepositoryOwner *struct {
			ProjectV2 *struct {
				ID    string `json:"id"`
				Field *struct {
					ID      string `json:"id"`
					Options []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"options"`
				} `json:"field"`
			} `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	err := t.graphql(ctx, boardQuery, map[string]any{"owner": p.Owner, "number": p.Number, "field": p.field()}, &out)
	switch {
	case err != nil:
		return nil, fmt.Errorf("project %d of %s: %w", p.Number, p.Owner, err)
	case out.RepositoryOwner == nil || out.RepositoryOwner.ProjectV2 == nil:
		return nil, fmt.Errorf("no project %d of %s that the token can see", p.Number, p.Owner)
	case out.RepositoryOwner.ProjectV2.Field == nil || out.RepositoryOwner.ProjectV2.Field.ID == "":
		return nil, fmt.Errorf("project %d of %s has no single-select field %q", p.Number, p.Owner, p.field())
	}
	pr := out.RepositoryOwner.ProjectV2
	b := &board{project: pr.ID, field: pr.Field.ID, options: map[string]string{}}
	for _, o := range pr.Field.Options {
		b.options[o.Name] = o.ID
	}
	t.boardIDs = b
	return b, nil
}

// option finds the ID of the field's option named name, leniently.
func (b *board) option(name string) (string, bool) {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	for n, id := range b.options {
		if norm(n) == norm(name) {
			return id, true
		}
	}
	return "", false
}

func (b *board) optionNames() string {
	var names []string
	for n := range b.options {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

const addItem = `mutation($project: ID!, $content: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $content}) { item { id } }
}`

const setField = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }
}`

// moveOnBoard puts the issue in the stage's column, adding it to the
// project if it isn't there.
func (t *Tracker) moveOnBoard(ctx context.Context, n string, is *issue, stage tracker.Stage) error {
	p := t.opts.Project
	if p == nil {
		return nil
	}
	column, ok := p.Columns[stage]
	if !ok || column == "" {
		return nil
	}
	b, err := t.board(ctx)
	if err != nil {
		return err
	}
	opt, ok := b.option(column)
	if !ok {
		return fmt.Errorf("project %d of %s: %s has no option %q (it has: %s)", p.Number, p.Owner, p.field(), column, b.optionNames())
	}
	// Adding an issue that's already on the project returns its item.
	var added struct {
		Add struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := t.graphql(ctx, addItem, map[string]any{"project": b.project, "content": is.NodeID}, &added); err != nil {
		return fmt.Errorf("adding #%s to project %d: %w", n, p.Number, err)
	}
	vars := map[string]any{"project": b.project, "item": added.Add.Item.ID, "field": b.field, "option": opt}
	if err := t.graphql(ctx, setField, vars, nil); err != nil {
		return fmt.Errorf("moving #%s to %q on project %d: %w", n, column, p.Number, err)
	}
	return nil
}

// checkBoard verifies that the project, its field and every column exist.
func (t *Tracker) checkBoard(ctx context.Context) error {
	p := t.opts.Project
	if p.Owner == "" || p.Number == 0 {
		return fmt.Errorf("Project needs Owner and Number")
	}
	b, err := t.board(ctx)
	if err != nil {
		return err
	}
	var missing []string
	for stage, c := range p.Columns {
		if _, ok := b.option(c); c != "" && !ok {
			missing = append(missing, fmt.Sprintf("%q (stage %s)", c, stage))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("project %d of %s: %s has no option %s (it has: %s)", p.Number, p.Owner, p.field(), strings.Join(missing, ", "), b.optionNames())
	}
	return nil
}
