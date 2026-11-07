package main

import (
	"bytes"
	"strings"
	"testing"

	"doexam/config"
)

func TestRunCommand(t *testing.T) {
	cfg := config.DoExamConfig{Environment: "development"}
	tests := []struct {
		name        string
		args        []string
		wantHandled bool
		wantCode    int
		wantOut     string
		wantErr     string
	}{
		{name: "no args serves", args: nil, wantHandled: false},
		{name: "serve", args: []string{"serve"}, wantHandled: false},
		{name: "version", args: []string{"version"}, wantHandled: true, wantOut: "dev"},
		{name: "help", args: []string{"--help"}, wantHandled: true, wantOut: "doexam migrate up"},
		{name: "unknown", args: []string{"bogus"}, wantHandled: true, wantCode: 2, wantErr: `unknown command "bogus"`},
		{name: "migrate without action", args: []string{"migrate"}, wantHandled: true, wantCode: 2, wantErr: "Usage"},
		// the config is checked before connecting (SessionTTL 0 is invalid here)
		{name: "migrate with bad config", args: []string{"migrate", "up"}, wantHandled: true, wantCode: 1, wantErr: "config error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			handled, code := runCommand(tt.args, cfg, &out, &errOut)
			if handled != tt.wantHandled || code != tt.wantCode {
				t.Fatalf("handled=%v code=%d, want %v %d (stderr: %s)", handled, code, tt.wantHandled, tt.wantCode, errOut.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errOut.String(), tt.wantErr) {
				t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
			}
		})
	}
}
