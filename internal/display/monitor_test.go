package display

import (
	"bytes"
	"strings"
	"testing"
)

var testMonitors = []Info{
	{Index: 0, Name: "eDP-1", Width: 1512, Height: 982, Scale: 2},
	{Index: 1, Name: "HDMI-1", Width: 1920, Height: 1080, Scale: 1},
	{Index: 2, Name: "DP-1", Width: 2560, Height: 1440, Scale: 1},
	{Index: 3, Name: "DP-2", Width: 2560, Height: 1440, Scale: 1},
}

func TestResolve(t *testing.T) {
	for _, tt := range []struct {
		spec    string
		want    int
		wantErr string
	}{
		{"", 0, ""},
		{"0", 0, ""},
		{" 2 ", 2, ""},
		{"hdmi-1", 1, ""},      // exact, case-insensitive
		{"HDMI", 1, ""},        // unique substring
		{"DP-1", 2, ""},        // exact match wins over substring "eDP-1"
		{"edp", 0, ""},         // substring
		{"4", 0, "only 4"},     // out of range
		{"-1", 0, "only 4"},    // negative index
		{"DP", 0, "ambiguous"}, // eDP-1, DP-1, DP-2
		{"VGA", 0, "no monitor"},
	} {
		got, err := resolve(tt.spec, testMonitors)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("resolve(%q) err = %v, want %q", tt.spec, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("resolve(%q) = %d, %v; want %d", tt.spec, got, err, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	var buf bytes.Buffer
	Describe(&buf, testMonitors[:2])
	out := buf.String()
	for _, want := range []string{"0: eDP-1", "(primary)", "1: HDMI-1", "1920x1080", "--monitor HDMI-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe output missing %q:\n%s", want, out)
		}
	}
}
