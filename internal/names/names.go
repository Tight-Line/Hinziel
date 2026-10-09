// Package names turns ticket keys and titles into branch and environment names.
package names

import "strings"

// Slug lowercases s, replaces every run of characters other than [a-z0-9]
// with a single dash, trims dashes from both ends and cuts the result to at
// most max bytes (0 = no limit), never ending on a dash.
func Slug(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	out := b.String()
	if max > 0 && len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// Branch is the default run branch name: skrat/<key>-<title>.
func Branch(key, title string) string {
	name := Slug(key, 0)
	if t := Slug(title, 30); t != "" {
		name += "-" + t
	}
	return "skrat/" + name
}

// Env is the default environment name: sk-<factory>-<key>, a DNS label of at
// most 63 characters.
func Env(factory, key string) string {
	return Slug("sk-"+factory+"-"+key, 63)
}
