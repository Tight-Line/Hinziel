// Package remote runs commands on and copies files to an environment's hosts
// over SSH. It uses the system's ssh client, so keys, agents and ssh_config
// work as they do on the command line.
//
// Components that deploy to servers use it; it's public so that components
// outside this repository can use it too.
package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tight-Line/heinzel/env"
)

// Host is one SSH-reachable machine.
type Host struct {
	Address        string
	User           string
	Port           int    // 0 = 22
	KeyPath        string // "" = ssh's defaults and agent
	KnownHostsPath string // "" = ssh's default; new hosts are accepted on first use and pinned
	// SSH is the ssh command; nil = {"ssh"}. Tests replace it.
	SSH []string
}

// FromEnv returns the SSH access of an environment host.
func FromEnv(h env.Host) (*Host, error) {
	if h.SSH == nil {
		return nil, fmt.Errorf("host %s (%s) has no SSH access", h.Name, h.Address)
	}
	return &Host{
		Address:        h.Address,
		User:           h.SSH.User,
		Port:           h.SSH.Port,
		KeyPath:        h.SSH.KeyPath,
		KnownHostsPath: h.SSH.KnownHostsPath,
	}, nil
}

func (h *Host) command(ctx context.Context, script string) *exec.Cmd {
	ssh := h.SSH
	if len(ssh) == 0 {
		ssh = []string{"ssh"}
	}
	args := append([]string{}, ssh[1:]...)
	args = append(args,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
	)
	if h.KnownHostsPath != "" {
		args = append(args, "-o", "UserKnownHostsFile="+h.KnownHostsPath)
	}
	if h.KeyPath != "" {
		args = append(args, "-i", h.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	if h.Port != 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	target := h.Address
	if h.User != "" {
		target = h.User + "@" + h.Address
	}
	// ssh hands its command line to the remote user's shell; wrapping the
	// script in `sh -c` makes it run the same whatever that shell is.
	args = append(args, target, "sh -c "+Quote(script))
	return exec.CommandContext(ctx, ssh[0], args...)
}

// Run runs a POSIX shell script on the host. Its output goes to out (nil =
// discarded); on failure the error carries the end of the output.
func (h *Host) Run(ctx context.Context, script string, stdin io.Reader, out io.Writer) error {
	cmd := h.command(ctx, script)
	tail := &tailBuffer{max: 4096}
	w := io.Writer(tail)
	if out != nil {
		w = io.MultiWriter(out, tail)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: %w\n%s", h.Address, err, strings.TrimSpace(tail.String()))
	}
	return nil
}

// Output runs a script and returns its standard output.
func (h *Host) Output(ctx context.Context, script string) (string, error) {
	cmd := h.command(ctx, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %w\n%s", h.Address, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// WriteFile writes data to path on the host, creating its directory.
func (h *Host) WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	script := fmt.Sprintf("set -e; mkdir -p %s; cat > %s.tmp; chmod %o %s.tmp; mv %s.tmp %s",
		Quote(dir), Quote(path), mode.Perm(), Quote(path), Quote(path), Quote(path))
	return h.Run(ctx, script, bytes.NewReader(data), nil)
}

// Upload replaces remoteDir on the host with the contents of localDir.
// Entries whose path relative to localDir is in exclude (or below one) are
// left out. The new tree is unpacked next to the old one and swapped in, so
// a failed upload leaves the previous tree alone.
func (h *Host) Upload(ctx context.Context, localDir, remoteDir string, exclude ...string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTarGz(pw, localDir, exclude)) }()
	next := remoteDir + ".heinzel-new"
	script := fmt.Sprintf("set -e; rm -rf %[1]s; mkdir -p %[1]s; tar -xzf - -C %[1]s; rm -rf %[2]s; mv %[1]s %[2]s",
		Quote(next), Quote(remoteDir))
	err := h.Run(ctx, script, pr, nil)
	pr.Close()
	return err
}

// WaitReady retries a no-op command until SSH answers or ctx is done.
func (h *Host) WaitReady(ctx context.Context, interval time.Duration) error {
	for {
		err := h.Run(ctx, "true", nil, nil)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for SSH on %s: %w (last error: %v)", h.Address, ctx.Err(), err)
		case <-time.After(interval):
		}
	}
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeTarGz(w io.Writer, root string, exclude []string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, ex := range exclude {
			if rel == ex || strings.HasPrefix(rel, ex+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	return errors.Join(err, tw.Close(), gz.Close())
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
