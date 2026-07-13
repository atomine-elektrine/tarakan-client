package main

import "testing"

func TestPeelAPIFlags(t *testing.T) {
	url, token, rest := peelAPIFlags([]string{
		"--token", "secret", "report", "--agent", "grok", "--url", "http://localhost:4000", "--pickup",
	})
	if url != "http://localhost:4000" || token != "secret" {
		t.Fatalf("url=%q token=%q", url, token)
	}
	if len(rest) != 4 || rest[0] != "report" || rest[3] != "--pickup" {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestResolveAPIFlagURL(t *testing.T) {
	got, err := resolveAPIFlagURL("", "https://tarakan.lol")
	if err != nil || got != "https://tarakan.lol" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := resolveAPIFlagURL("https://a", "https://b"); err == nil {
		t.Fatal("expected disagreement error")
	}
}
