package naming

import (
	"fmt"
	"regexp"
	"strings"
)

var separators = regexp.MustCompile(`[-_. ]+`)

// Normalize mechanically converts a repository basename into an mstore key.
func Normalize(repo string) (string, error) {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	repo = separators.ReplaceAllString(repo, "-")
	repo = strings.Trim(repo, "-")
	if repo == "" {
		return "", fmt.Errorf("model name is empty after normalization")
	}
	if len(repo) > 64 {
		return "", fmt.Errorf("model name %q is %d bytes; maximum is 64 (use --name)", repo, len(repo))
	}
	for _, r := range repo {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return "", fmt.Errorf("model name %q contains non-ASCII or unsupported characters", repo)
		}
	}
	return repo, nil
}

func Validate(name string) error {
	n, err := Normalize(name)
	if err != nil {
		return err
	}
	if n != name || strings.Contains(name, "/") {
		return fmt.Errorf("invalid model name %q; expected %q", name, n)
	}
	return nil
}
