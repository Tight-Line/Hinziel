package trello

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Tight-Line/skrat/tracker"
)

// Stage says how a lifecycle stage shows on a card: moved to a list, a label
// added, both, or neither. Boards name their lists and labels in many ways,
// so names match ignoring case and surrounding or repeated spaces ("in
// Progress " matches "In progress"). A list or label ID works too, and keeps
// working when the list is renamed.
//
// Moving a card to a stage also takes off the labels of every other stage,
// so a "Blocked" label disappears once the run continues.
type Stage struct {
	List  string // move the card to this list; "" = leave it where it is
	Label string // put this label on the card; "" = none
}

// normalize folds case and spaces, the ways the same name tends to differ.
func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// resolve finds want among a board's items by ID or normalized name. Two
// items with the same name are an error rather than a guess.
func resolve(kind, want string, ids, names []string) (string, error) {
	var found []string
	for i := range ids {
		if ids[i] == want {
			return ids[i], nil
		}
		if normalize(names[i]) == normalize(want) {
			found = append(found, ids[i])
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		var available []string
		for _, n := range names {
			if strings.TrimSpace(n) != "" {
				available = append(available, fmt.Sprintf("%q", n))
			}
		}
		return "", fmt.Errorf("no %s %q on the board (it has: %s)", kind, want, strings.Join(available, ", "))
	}
	return "", fmt.Errorf("%d %ss are named %q; use the ID of the one you mean", len(found), kind, want)
}

func (t *Tracker) resolveList(ctx context.Context, board, want string) (string, error) {
	lists, err := t.boardLists(ctx, board)
	if err != nil {
		return "", err
	}
	var ids, names []string
	for _, l := range lists {
		ids, names = append(ids, l.ID), append(names, l.Name)
	}
	return resolve("list", want, ids, names)
}

func (t *Tracker) resolveLabel(ctx context.Context, board, want string) (string, error) {
	labels, err := t.boardLabels(ctx, board)
	if err != nil {
		return "", err
	}
	var ids, names []string
	for _, l := range labels {
		ids, names = append(ids, l.ID), append(names, l.Name)
	}
	return resolve("label", want, ids, names)
}

func (t *Tracker) boardLabels(ctx context.Context, board string) ([]label, error) {
	t.mu.Lock()
	cached, ok := t.labels[board]
	t.mu.Unlock()
	if ok {
		return cached, nil
	}
	var labels []label
	q := url.Values{"fields": {"name"}, "limit": {"1000"}}
	if err := t.api.Do(ctx, http.MethodGet, "/boards/"+url.PathEscape(board)+"/labels", q, nil, &labels); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.labels[board] = labels
	t.mu.Unlock()
	return labels, nil
}

// MoveTo makes the card show stage: it moves it to the stage's list, adds the
// stage's label and takes off the other stages' labels. Without any Stages it
// does nothing.
func (t *Tracker) MoveTo(ctx context.Context, id string, stage tracker.Stage) error {
	if len(t.opts.Stages) == 0 {
		return nil
	}
	var c struct {
		IDBoard  string   `json:"idBoard"`
		IDList   string   `json:"idList"`
		IDLabels []string `json:"idLabels"`
	}
	path := "/cards/" + url.PathEscape(cardID(id))
	if err := t.api.Do(ctx, http.MethodGet, path, url.Values{"fields": {"idBoard,idList,idLabels"}}, nil, &c); err != nil {
		return err
	}
	want := t.opts.Stages[stage]

	if want.List != "" {
		list, err := t.resolveList(ctx, c.IDBoard, want.List)
		if err != nil {
			return fmt.Errorf("stage %s: %w", stage, err)
		}
		if list != c.IDList {
			if err := t.api.Do(ctx, http.MethodPut, path, nil, map[string]string{"idList": list}, nil); err != nil {
				return err
			}
		}
	}

	keep := ""
	if want.Label != "" {
		label, err := t.resolveLabel(ctx, c.IDBoard, want.Label)
		if err != nil {
			return fmt.Errorf("stage %s: %w", stage, err)
		}
		keep = label
		if !slices.Contains(c.IDLabels, label) {
			if err := t.api.Do(ctx, http.MethodPost, path+"/idLabels", nil, map[string]string{"value": label}, nil); err != nil {
				return err
			}
		}
	}
	for other, s := range t.opts.Stages {
		if other == stage || s.Label == "" {
			continue
		}
		label, err := t.resolveLabel(ctx, c.IDBoard, s.Label)
		if err != nil {
			return fmt.Errorf("stage %s: %w", other, err)
		}
		if label != keep && slices.Contains(c.IDLabels, label) {
			if err := t.api.Do(ctx, http.MethodDelete, path+"/idLabels/"+label, nil, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkStages resolves every stage's list and label on the board, reporting
// all that don't resolve, each with the names the board does have.
func (t *Tracker) checkStages(ctx context.Context, board string) error {
	var errs []error
	for _, stage := range slices.Sorted(maps.Keys(t.opts.Stages)) {
		s := t.opts.Stages[stage]
		if s.List != "" {
			if _, err := t.resolveList(ctx, board, s.List); err != nil {
				errs = append(errs, fmt.Errorf("stage %s: %w", stage, err))
			}
		}
		if s.Label != "" {
			if _, err := t.resolveLabel(ctx, board, s.Label); err != nil {
				errs = append(errs, fmt.Errorf("stage %s: %w", stage, err))
			}
		}
	}
	return errors.Join(errs...)
}
