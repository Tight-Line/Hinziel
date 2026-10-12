package hostinstall

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// envVar is one line of an env file.
type envVar struct{ Name, Value string }

// readEnvFile reads KEY=VALUE lines, as a shell sourcing the file would for
// plain assignments: blank lines and comments are skipped, an `export ` prefix
// and matching quotes around the value are removed.
func readEnvFile(path string) ([]envVar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var vars []envVar
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("%s:%d: not a KEY=VALUE line", path, n)
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		vars = append(vars, envVar{name, value})
	}
	return vars, sc.Err()
}

// file is a local file the factory reads, named by a variable's value.
type file struct{ Local, Remote string }

// relocate finds variables whose value is a file on this machine (a key, a
// certificate), points them at a copy under dir and returns the files to
// copy. A private key's .pub next to it comes along.
func relocate(vars []envVar, dir string) ([]envVar, []file) {
	var files []file
	taken := map[string]bool{}
	add := func(local string) string {
		base := filepath.Base(local)
		for i := 2; taken[base]; i++ {
			base = fmt.Sprintf("%d-%s", i, filepath.Base(local))
		}
		taken[base] = true
		remote := dir + "/" + base
		files = append(files, file{local, remote})
		return remote
	}
	out := slices.Clone(vars)
	for i, v := range out {
		if !filepath.IsAbs(v.Value) {
			continue
		}
		if st, err := os.Stat(v.Value); err != nil || !st.Mode().IsRegular() {
			continue
		}
		out[i].Value = add(v.Value)
		if st, err := os.Stat(v.Value + ".pub"); err == nil && st.Mode().IsRegular() {
			add(v.Value + ".pub")
		}
	}
	return out, files
}

// format writes vars in the format of `docker run --env-file`: one
// KEY=VALUE per line, taken literally.
func format(vars []envVar) (string, error) {
	var b strings.Builder
	for _, v := range vars {
		if strings.ContainsAny(v.Value, "\n\r") {
			return "", fmt.Errorf("%s spans several lines, which an env file can't hold; put it in a file and give its path", v.Name)
		}
		fmt.Fprintf(&b, "%s=%s\n", v.Name, v.Value)
	}
	return b.String(), nil
}
