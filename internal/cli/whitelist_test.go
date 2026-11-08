package cli

import (
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func TestWhitelist(t *testing.T) {
	e := env.New(t.TempDir())

	out, _, code := sapu(t, e, "", "whitelist", "list")
	if code != 0 || !strings.Contains(out, "whitelist is empty") {
		t.Errorf("exit %d\n%s", code, out)
	}

	out, _, code = sapu(t, e, "", "whitelist", "add", "~/Documents/keep")
	if code != 0 || !strings.Contains(out, "Added ~/Documents/keep") {
		t.Errorf("add: exit %d\n%s", code, out)
	}
	sapu(t, e, "", "whitelist", "add", "~/Library/Caches/typescript")
	sapu(t, e, "", "whitelist", "add", "~/Documents/keep") // twice is fine

	out, _, _ = sapu(t, e, "", "whitelist", "list")
	if out != "~/Documents/keep\n~/Library/Caches/typescript\n" {
		t.Errorf("list:\n%s", out)
	}

	out, _, code = sapu(t, e, "", "whitelist", "remove", "~/Documents/keep")
	if code != 0 || !strings.Contains(out, "Removed ~/Documents/keep") {
		t.Errorf("remove: exit %d\n%s", code, out)
	}
	out, _, _ = sapu(t, e, "", "whitelist", "list")
	if out != "~/Library/Caches/typescript\n" {
		t.Errorf("list after remove:\n%s", out)
	}
}

func TestWhitelistErrors(t *testing.T) {
	e := env.New(t.TempDir())
	for _, args := range [][]string{
		{"whitelist", "add"},
		{"whitelist", "add", "a", "b"},
		{"whitelist", "remove"},
		{"whitelist", "list", "extra"},
	} {
		if _, _, code := sapu(t, e, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	// Not a path Sapu can match: protect refuses it.
	if _, _, code := sapu(t, e, "", "whitelist", "add", "relative/path"); code != 1 {
		t.Errorf("relative path: exit %d, want 1", code)
	}
	out, _, code := sapu(t, e, "", "whitelist", "add", "relative/path", "--json")
	if code != 1 || !strings.Contains(out, `"ok": false`) {
		t.Errorf("--json error: exit %d\n%s", code, out)
	}
	validate(t, "whitelist-list", out)
}

func TestWhitelistJSON(t *testing.T) {
	e := env.New(t.TempDir())
	for _, args := range [][]string{
		{"whitelist", "list", "--json"},
		{"whitelist", "add", "~/Documents", "--json"},
		{"whitelist", "list", "--json"},
		{"whitelist", "remove", "~/Documents", "--json"},
	} {
		out, _, code := sapu(t, e, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		validate(t, "whitelist-list", out)
	}
	out, _, _ := sapu(t, e, "", "whitelist", "add", "~/Documents", "--json")
	var d whitelistData
	decodeData(t, out, &d)
	if len(d.Entries) != 1 || d.Entries[0] != "~/Documents" {
		t.Errorf("entries %v", d.Entries)
	}
}
