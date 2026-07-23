package source

import "testing"

func TestParseRefRejectsControlCharacters(t *testing.T) {
	for _, ref := range []string{
		"hf:Acme/Widget\nignored@main",
		"ms:Acme/Widget@main\nignored",
	} {
		if _, err := ParseRef(ref); err == nil {
			t.Fatalf("ParseRef(%q) unexpectedly succeeded", ref)
		}
	}
}
