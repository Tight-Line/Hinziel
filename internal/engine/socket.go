package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
)

// The agent's `deploy` and `retrieve` commands reach the executing engine
// over a Unix socket, so only the engine touches run state.

type socketServer struct {
	path string
	srv  *http.Server
	once sync.Once
}

type socketReply struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// serveSocket starts the socket for the duration of implement. It lives in
// the agent's directory, which the sandbox mounts, unless that path is too
// long for a Unix socket (~108 bytes).
func (x *execution) serveSocket(ctx context.Context) (*socketServer, error) {
	path := filepath.Join(x.agentDir(), "hinziel.sock")
	if len(path) > 100 {
		path = filepath.Join(os.TempDir(), "hinziel-"+string(x.r.ID)+".sock")
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /deploy", x.handle(x.agentDeploy))
	mux.HandleFunc("POST /retrieve", x.handle(x.agentRetrieve))
	s := &socketServer{path: path, srv: &http.Server{
		Handler:     mux,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}}
	go s.srv.Serve(ln)
	return s, nil
}

// close stops accepting requests and waits for in-flight ones. Safe to call twice.
func (s *socketServer) close() {
	s.once.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.srv.Shutdown(shutdownCtx)
		os.Remove(s.path)
	})
}

func (x *execution) handle(f func(ctx context.Context) ([]string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		x.socketMu.Lock()
		defer x.socketMu.Unlock()
		lines, err := f(req.Context())
		reply := socketReply{Output: strings.Join(lines, "\n")}
		if err != nil {
			reply.Error = err.Error()
			w.WriteHeader(http.StatusConflict)
		}
		json.NewEncoder(w).Encode(reply)
	}
}

// agentDeploy deploys every repo's pushed HEAD, refusing uncommitted or
// unpushed work, and changes in the environment that aren't in git yet.
func (x *execution) agentDeploy(ctx context.Context) ([]string, error) {
	for _, cfg := range x.p.Repos {
		if err := x.checkCommittedAndPushed(ctx, cfg.Name); err != nil {
			return nil, err
		}
	}
	for _, d := range x.retrievers() {
		ws, err := x.repo(ctx, d.Repo)
		if err != nil {
			return nil, err
		}
		if err := d.Deployer.(deploy.Retriever).Retrieve(ctx, x.r.Environment, ws.Dir); err != nil {
			return nil, fmt.Errorf("deployer %s: retrieve: %w", d.Name, err)
		}
		if dirty, err := ws.Dirty(ctx); err != nil || dirty {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("the environment has changes not in git (repo %s); review, commit and push them first", d.Repo)
		}
	}
	return x.deployWaiting(ctx)
}

// deployWaiting deploys for the agent's deploy command. The agent is blocked
// on the command anyway, so a deployer that has to wait (for CI to build an
// image, say) is waited for here, within the step's time, rather than
// parking the run. Deployers already done aren't deployed again on a retry.
func (x *execution) deployWaiting(ctx context.Context) ([]string, error) {
	var lines []string
	for skip := false; ; skip = true {
		report, err := x.deployAll(ctx, skip)
		lines = append(lines, report...)
		if err == nil {
			err = x.checkConfig()
		}
		var wait *core.WaitError
		if !errors.As(err, &wait) {
			return lines, err
		}
		after := wait.After
		if after <= 0 {
			after = time.Minute
		}
		lines = append(lines, fmt.Sprintf("waiting for %s; checking again in %s", wait.Reason, after))
		x.logf("", "deploy: waiting for %s", wait.Reason)
		select {
		case <-ctx.Done():
			return lines, ctx.Err()
		case <-time.After(after):
		}
	}
}

func (x *execution) checkCommittedAndPushed(ctx context.Context, repo string) error {
	ws, err := x.repo(ctx, repo)
	if err != nil {
		return err
	}
	dirty, err := ws.Dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("repo %s has uncommitted changes; commit and push them first", repo)
	}
	pushed, err := ws.IsPushed(ctx)
	if err != nil {
		return err
	}
	if !pushed {
		return fmt.Errorf("repo %s: HEAD is not pushed; push to %s first", repo, x.r.Branch)
	}
	return nil
}

// agentRetrieve pulls changes made in the environment into the working trees.
func (x *execution) agentRetrieve(ctx context.Context) ([]string, error) {
	var lines []string
	for _, d := range x.retrievers() {
		ws, err := x.repo(ctx, d.Repo)
		if err != nil {
			return lines, err
		}
		if err := d.Deployer.(deploy.Retriever).Retrieve(ctx, x.r.Environment, ws.Dir); err != nil {
			return lines, fmt.Errorf("deployer %s: retrieve: %w", d.Name, err)
		}
		lines = append(lines, fmt.Sprintf("%s: retrieved into %s", d.Name, ws.Dir))
	}
	if len(lines) == 0 {
		lines = append(lines, "no deployer retrieves changes from the environment")
	}
	return lines, nil
}

// Call sends an agent command ("deploy" or "retrieve") to the engine
// listening on socket and returns its output.
func Call(ctx context.Context, socket, command string) (string, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hinziel/"+command, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching the run's engine: %w", err)
	}
	defer resp.Body.Close()
	var reply socketReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", fmt.Errorf("reading the engine's reply: %w", err)
	}
	if reply.Error != "" {
		return reply.Output, errors.New(reply.Error)
	}
	return reply.Output, nil
}
