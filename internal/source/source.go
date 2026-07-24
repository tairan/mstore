package source

import (
	"fmt"
	"strings"
	"unicode"
)

type Model struct {
	Provider  string `json:"provider"`
	Repo      string `json:"repo"`
	Revision  string `json:"revision"`
	Path      string `json:"-"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Preferred bool   `json:"-"`
}

func (m Model) Ref() string { return m.Provider + ":" + m.Repo + "@" + m.Revision }

type Ref struct {
	Provider string
	Repo     string
	Revision string
}

func ParseRef(s string) (Ref, error) {
	var r Ref
	i := strings.IndexByte(s, ':')
	if i < 1 {
		return r, fmt.Errorf("source must start with hf: or ms:")
	}
	r.Provider, s = s[:i], s[i+1:]
	if r.Provider != "hf" && r.Provider != "ms" {
		return r, fmt.Errorf("unknown provider %q", r.Provider)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return r, fmt.Errorf("repository and revision must not contain control characters")
	}
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		r.Repo, r.Revision = s[:at], s[at+1:]
		if r.Revision == "" {
			return r, fmt.Errorf("revision after @ must not be empty")
		}
	} else {
		r.Repo = s
	}
	if strings.Count(r.Repo, "/") != 1 || r.Repo == "" {
		return r, fmt.Errorf("repository must be namespace/name")
	}
	for _, part := range strings.Split(r.Repo, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\`) {
			return r, fmt.Errorf("invalid repository path")
		}
	}
	return r, nil
}

func Match(m Model, r Ref) bool {
	return m.Provider == r.Provider && m.Repo == r.Repo &&
		(r.Revision == "" || m.Revision == r.Revision || strings.HasPrefix(m.Revision, r.Revision))
}
