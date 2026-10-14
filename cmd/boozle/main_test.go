package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoArgsPrintsFriendlyHint(t *testing.T) {
	cmd := newRootCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if !errors.Is(err, errNoArgs) {
		t.Fatalf("Execute error = %v, want errNoArgs", err)
	}
	out := stderr.String()
	for _, want := range []string{
		"boozle: no PDF given.",
		"Fast starts:",
		"Common playback flags:",
		"Displays:",
		"Sidecars and notes:",
		"-P, --presenter-monitor <N|name>",
		"--bg <#hex>",
		"--config <path>",
		"cache_mb",
		"render_scale",
		"slides.boozle.toml",
		"boozle notes import deck.pptx",
		"boozle slides.pdf --auto 30s --progress",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("hint missing %q:\n%s", want, out)
		}
	}
}

func TestHelpIncludesReleaseFeatures(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute --help: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Common examples",
		"Audience display",
		"--transition",
		"--presenter-monitor",
		"--config",
		"PowerPoint speaker-note import",
		"Sidecars",
		"Supported TOML keys",
		"presenter_monitor",
		"cache_mb",
		"render_scale",
		"[[page]]",
		"notes",
		"Speaker notes",
		"Presenter view",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
}

func TestNotesHelpDescribesSidecars(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"notes", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute notes --help: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"speaker notes sidecars",
		".boozle.toml",
		".pdfpc",
		"boozle notes import",
		"PowerPoint speaker notes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notes help missing %q:\n%s", want, text)
		}
	}
}

func TestNotesImportHelpIncludesFlags(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"notes", "import", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute notes import --help: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"--out",
		"--config",
		"--force",
		"file.pptx",
		"<file>.boozle.toml",
		"[[page]] notes",
		"not needed at presentation time",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notes import help missing %q:\n%s", want, text)
		}
	}
}

// TestDefaultVersionMatchesChangelog keeps the built-in version (used by
// source builds such as Homebrew's) in step with the newest release in
// CHANGELOG.md.
func TestDefaultVersionMatchesChangelog(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindSubmatch(data)
	if m == nil {
		t.Fatal("no released version heading in CHANGELOG.md")
	}
	if want := "v" + string(m[1]); version != want {
		t.Fatalf("version = %q, but the newest CHANGELOG release is %s", version, want)
	}
}
