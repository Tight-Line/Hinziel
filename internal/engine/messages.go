package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/core/remote"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
)

// prompt is the task description given to the agent.
func prompt(p *Project, r *run.Run, task *agent.Task) string {
	t := r.Ticket
	var b strings.Builder
	fmt.Fprintf(&b, "Implement ticket %s: %s\n", t.Key, t.Title)
	if t.URL != "" {
		fmt.Fprintf(&b, "Ticket: %s\n", t.URL)
	}
	if d := strings.TrimSpace(t.Description); d != "" {
		fmt.Fprintf(&b, "\n## Description\n\n%s\n", d)
	}
	for _, name := range sortedKeys(t.Fields) {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", name, t.Fields[name])
	}
	if len(t.Comments) > 0 {
		b.WriteString("\n## Comments on the ticket\n")
		for _, c := range t.Comments {
			fmt.Fprintf(&b, "\n**%s:**\n%s\n", c.Author, c.Body)
		}
	}
	if len(t.Attachments) > 0 {
		b.WriteString("\n## Attachments\n\n")
		for _, a := range t.Attachments {
			if a.Path != "" {
				fmt.Fprintf(&b, "- %s: %s (downloaded from the ticket; read it there)\n", a.Name, a.Path)
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.URL)
		}
	}

	b.WriteString("\n## Workspace\n\n")
	fmt.Fprintf(&b, "Every repository is checked out under %s on branch `%s`:\n\n", task.Workdir, r.Branch)
	for _, c := range task.Repos {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Path)
	}
	if e := task.Environment; e != nil {
		fmt.Fprintf(&b, "\nYour environment %q is running the code from before your changes.\n", e.Name)
		for _, name := range sortedKeys(e.Endpoints) {
			fmt.Fprintf(&b, "- %s: %s\n", name, e.Endpoints[name])
		}
		var shells []string
		for _, h := range e.Hosts {
			if h.SSH != nil {
				shells = append(shells, fmt.Sprintf("- %s: `%s`", h.Name, sshCommand(h, task.Sandbox != nil)))
			}
		}
		if len(shells) > 0 {
			b.WriteString("\nYou can run commands on its hosts over SSH, for example to run tests where the app is deployed:\n")
			b.WriteString(strings.Join(shells, "\n") + "\n")
		}
	}

	b.WriteString("\n## How to work\n\n")
	fmt.Fprintf(&b, "- Commit your work and push it to `%s` (a plain `git push` creates it on the remote). Push only the repositories you change; the others stay as they are. Never push to any other branch and never open merge requests; Hinziel does that.\n", r.Branch)
	if task.Environment != nil {
		b.WriteString("- To try your work, push it, then run `$HINZIEL_BIN deploy`. It deploys every repository's pushed HEAD to your environment and prints the result. Then test against the URLs above. Repeat as needed.\n")
		if slices.ContainsFunc(p.Deployers, isRetriever) {
			b.WriteString("- Changes you make directly in the environment are not in git. Run `$HINZIEL_BIN retrieve` to pull them into your checkout, then review, commit and push them. `$HINZIEL_BIN deploy` refuses to run while such changes are not committed.\n")
		}
	}
	b.WriteString("- If something must be done by a human (a setting you can't change, a secret to add), say so in your final summary under \"Manual steps\".\n")

	if p.Settings.Instructions != "" {
		fmt.Fprintf(&b, "\n## Instructions for this project\n\n%s\n", strings.TrimSpace(p.Settings.Instructions))
	}
	return b.String()
}

func isRetriever(d Deployer) bool {
	_, ok := d.Deployer.(deploy.Retriever)
	return ok
}

func commitMessage(r *run.Run) string {
	if r.Outcome != nil && strings.TrimSpace(r.Outcome.CommitMessage) != "" {
		return r.Outcome.CommitMessage
	}
	return fmt.Sprintf("%s: %s", r.Ticket.Key, r.Ticket.Title)
}

func noChangesQuestion(r *run.Run) string {
	q := "I didn't change anything in any repository."
	if r.Outcome != nil && strings.TrimSpace(r.Outcome.Summary) != "" {
		q += " Here is what I found:\n\n" + strings.TrimSpace(r.Outcome.Summary) + "\n\n"
	} else {
		q += " "
	}
	return q + "Could you clarify what should change? Answer in a comment, then resume the run."
}

// changeRequestMessage is the description of one repo's change request.
func changeRequestMessage(r *run.Run) core.Message {
	var b strings.Builder
	if r.Ticket.URL != "" {
		fmt.Fprintf(&b, "Implements [%s: %s](%s).\n", r.Ticket.Key, r.Ticket.Title, r.Ticket.URL)
	} else {
		fmt.Fprintf(&b, "Implements %s: %s.\n", r.Ticket.Key, r.Ticket.Title)
	}
	if r.Outcome != nil && strings.TrimSpace(r.Outcome.Summary) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(r.Outcome.Summary))
	}
	writeManualSteps(&b, r)
	writeActions(&b, r)
	fmt.Fprintf(&b, "\n_Opened by Hinziel, run `%s`._\n", r.ID)
	msg := core.Message{Body: b.String()}
	if r.Ticket.URL != "" {
		msg.Links = append(msg.Links, core.Link{Title: r.Ticket.Key, URL: r.Ticket.URL})
	}
	return msg
}

// relatedMessage lists the other change requests of the same run.
func relatedMessage(r *run.Run, repo string) core.Message {
	var b strings.Builder
	b.WriteString("This ticket also changed:\n")
	var links []core.Link
	for _, name := range sortedKeys(r.ChangeRequests) {
		if name == repo {
			continue
		}
		cr := r.ChangeRequests[name]
		fmt.Fprintf(&b, "- %s: %s\n", name, cr.URL)
		links = append(links, core.Link{Title: name, URL: cr.URL})
	}
	b.WriteString("\nThey may need to be merged together.\n")
	return core.Message{Body: b.String(), Links: links}
}

// publishMessage announces the result on the change requests and the ticket.
func publishMessage(r *run.Run) core.Message {
	var b strings.Builder
	var links []core.Link
	if len(r.ChangeRequests) > 0 {
		b.WriteString("**Changes**\n")
		for _, name := range sortedKeys(r.ChangeRequests) {
			cr := r.ChangeRequests[name]
			fmt.Fprintf(&b, "- %s: %s\n", name, cr.URL)
			links = append(links, core.Link{Title: "Merge request: " + name, URL: cr.URL})
		}
	}
	if r.Environment != nil && len(r.Environment.Endpoints) > 0 {
		b.WriteString("\n**Environment** `" + r.Environment.Name + "`\n")
		for _, name := range sortedKeys(r.Environment.Endpoints) {
			u := r.Environment.Endpoints[name]
			fmt.Fprintf(&b, "- %s: %s\n", name, u)
			links = append(links, core.Link{Title: "Preview: " + name, URL: u})
		}
	}
	writeManualSteps(&b, r)
	writeActions(&b, r)
	return core.Message{Title: "Hinziel: " + r.Ticket.Key + " is ready for review", Body: b.String(), Links: links}
}

func questionMessage(r *run.Run, question string) core.Message {
	return core.Message{
		Title: "Hinziel needs input",
		Body: question + fmt.Sprintf("\n\n_Run `%s` waits for your answer: reply here, mentioning me, if this factory takes answers from comments; "+
			"or run `answer %s <answer>`._\n", r.ID, r.ID),
	}
}

func writeManualSteps(b *strings.Builder, r *run.Run) {
	if r.Outcome == nil || len(r.Outcome.ManualSteps) == 0 {
		return
	}
	b.WriteString("\n**Manual steps**\n")
	for _, s := range r.Outcome.ManualSteps {
		fmt.Fprintf(b, "- [ ] %s\n", s)
	}
}

// eventMessage is the default text of a notification.
func eventMessage(r *run.Run, typ notify.EventType) core.Message {
	subject := subject(r)
	switch typ {
	case notify.EventRunStarted:
		return core.Message{Title: subject + ": started", Body: fmt.Sprintf("Run `%s` started.", r.ID)}
	case notify.EventRunSucceeded:
		msg := publishMessage(r)
		msg.Title = subject + ": ready for review"
		return msg
	case notify.EventRunFailed:
		return core.Message{Title: subject + ": failed", Body: fmt.Sprintf("Run `%s` failed at `%s`:\n\n%s\n\nResume it with `resume %s`.", r.ID, r.CurrentStep, firstLine(r.Error), r.ID)}
	case notify.EventRunWaiting:
		return core.Message{Title: subject + ": waiting", Body: fmt.Sprintf("Run `%s` is waiting for %s at `%s`. It checks again at %s; `resume %s` checks now.",
			r.ID, r.WaitingFor, r.CurrentStep, r.WaitUntil.UTC().Format("2006-01-02 15:04 MST"), r.ID)}
	case notify.EventRunNeedsInput:
		return core.Message{Title: subject + ": needs input", Body: r.Error}
	case notify.EventRunStepStarted:
		return stepMessage(r, subject)
	case notify.EventRunAwaitingApproval:
		var b strings.Builder
		fmt.Fprintf(&b, "Run `%s` is waiting before `%s`.", r.ID, r.CurrentStep)
		if pending := pendingActions(r, r.CurrentStep); len(pending) > 0 {
			b.WriteString(" These must be done first:\n\n")
			for _, a := range pending {
				fmt.Fprintf(&b, "- **%s**\n\n%s\n\n", a.Title, indent(strings.TrimSpace(a.Body)))
			}
			fmt.Fprintf(&b, "Then confirm with `approve %s`.", r.ID)
		} else {
			fmt.Fprintf(&b, " Continue it with `approve %s`.", r.ID)
		}
		return core.Message{Title: subject + ": waiting for approval", Body: b.String()}
	}
	return core.Message{Title: subject + ": " + string(typ)}
}

// stepTitles say what each built-in step does, for people following a run
// on its ticket. Other steps are named after themselves.
var stepTitles = map[string]string{
	StepFetchTicket:          "reading the ticket",
	StepPrepareWorkspace:     "preparing the branch",
	StepProvisionEnvironment: "creating the environment",
	StepDeployBase:           "deploying the current version to the environment",
	StepImplement:            "the agent is working on the change",
	StepRetrieve:             "pulling changes made in the environment into git",
	StepFinalizeBranch:       "finishing the branch",
	StepOpenChangeRequest:    "opening the merge request",
	StepDeploy:               "deploying the change to the environment",
	StepPublish:              "reporting the result",
}

// stepMessage announces that r's current step started, with where it is in
// the pipeline.
func stepMessage(r *run.Run, subject string) core.Message {
	step := r.CurrentStep
	title, ok := stepTitles[step]
	if !ok {
		title = strings.ReplaceAll(step, "_", " ")
	}
	body := fmt.Sprintf("Step `%s` of run `%s`.", step, r.ID)
	if i := slices.Index(Pipeline, step); i >= 0 {
		body = fmt.Sprintf("Step %d of %d (`%s`) of run `%s`.", i+1, len(Pipeline), step, r.ID)
	}
	return core.Message{Title: subject + ": " + title, Body: body}
}

// subject names a run in notification titles: "<factory> <ticket key>".
func subject(r *run.Run) string {
	key := r.Request.Ticket.ID
	switch {
	case r.Ticket != nil:
		key = r.Ticket.Key
	case envUp(r):
		key = "env up " + r.Request.EnvKey
	}
	return r.Factory + " " + key
}

// indent indents every line by two spaces, to nest text under a list item.
func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// sshCommand is the command that opens a shell on h, with its pinned host key.
// sshCommand is how the agent reaches h. In a sandbox, the factory's
// runtime directory doesn't exist, so the shared connection lives in /tmp.
func sshCommand(h env.Host, sandboxed bool) string {
	shared := remote.SharedConnection()
	if sandboxed {
		shared = []string{"-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/hinziel-ssh-%C", "-o", "ControlPersist=60"}
	}
	cmd := "ssh " + strings.Join(shared, " ")
	if h.SSH.KeyPath != "" {
		// Without IdentitiesOnly, ssh offers the agent's keys first, and a
		// server stops after a few: "Too many authentication failures".
		cmd += " -i " + h.SSH.KeyPath + " -o IdentitiesOnly=yes"
	}
	if h.SSH.KnownHostsPath != "" {
		cmd += " -o UserKnownHostsFile=" + h.SSH.KnownHostsPath
	}
	if h.SSH.Port != 0 && h.SSH.Port != 22 {
		cmd += fmt.Sprintf(" -p %d", h.SSH.Port)
	}
	target := h.Address
	if h.SSH.User != "" {
		target = h.SSH.User + "@" + target
	}
	return cmd + " " + target
}
