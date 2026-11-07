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
		{name: "tls without action", args: []string{"tls"}, wantHandled: true, wantCode: 2, wantErr: "Usage"},
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

func TestRunTLSInit(t *testing.T) {
	cfg := config.DoExamConfig{Environment: "development", TLSDir: t.TempDir()}
	var out, errOut bytes.Buffer
	handled, code := runCommand([]string{"tls", "init", "192.168.1.10", "exam.lab"}, cfg, &out, &errOut)
	if !handled || code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	for _, want := range []string{"created a local CA", "192.168.1.10, exam.lab", "TLS_CERT_FILE=", "CA fingerprint"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if _, code := runCommand([]string{"tls", "init", "exam.lab"}, cfg, &out, &errOut); code != 0 || !strings.Contains(out.String(), "kept the existing CA") {
		t.Fatalf("second run: code %d, %s", code, out.String())
	}
}
