package main

import (
	"bytes"
	"errors"
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
		"-P, --presenter-monitor <N>",
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
		"--transition",
		"--presenter-monitor",
		"Presenter view",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
}

func TestDefaultVersionIsReleaseVersion(t *testing.T) {
	if version != "v1.1.0" {
		t.Fatalf("version = %q, want v1.1.0", version)
	}
}
