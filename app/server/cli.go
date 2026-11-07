package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"doexam/config"
	"doexam/migrations"
	"doexam/pkg/dbmigrate"
	"doexam/pkg/tlsutil"
)

const usage = `DoExam server

Usage:
  doexam [serve]            run the server (default)
  doexam migrate up         apply pending database migrations
  doexam migrate down N     roll back the last N migrations
  doexam migrate version    show the current schema version
  doexam tls init [host…]   create a local CA (once) and an HTTPS certificate in TLS_DIR
                            for the given host names/IPs (default: this machine's names and IPs)
  doexam version            print the build version

Configuration comes from environment variables (see .env.example).
`

// runCommand handles the subcommands. It returns handled=false for "serve" (or no
// argument), so main goes on to start the server.
func runCommand(args []string, cfg config.DoExamConfig, stdout, stderr io.Writer) (handled bool, code int) {
	if len(args) == 0 || args[0] == "serve" {
		return false, 0
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version)
		return true, 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return true, 0
	case "migrate":
		return true, runMigrate(args[1:], cfg, stdout, stderr)
	case "tls":
		return true, runTLS(args[1:], cfg, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return true, 2
	}
}

func runMigrate(args []string, cfg config.DoExamConfig, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 1
	}
	m, err := dbmigrate.New(migrations.FS, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if err := m.Close(); err != nil {
			fmt.Fprintln(stderr, err)
		}
	}()

	switch args[0] {
	case "up":
		changed, err := m.Up()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		st, err := m.Status()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if changed {
			fmt.Fprintf(stdout, "migrated to version %d\n", st.Version)
		} else {
			fmt.Fprintf(stdout, "already at version %d\n", st.Version)
		}
		return 0
	case "down":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "migrate down needs the number of migrations to roll back, e.g. `doexam migrate down 1`")
			return 2
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintf(stderr, "invalid number %q\n", args[1])
			return 2
		}
		done, err := m.Down(n)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "rolled back %d migration(s)\n", done)
		return 0
	case "version":
		st, err := m.Status()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		switch {
		case st.None:
			fmt.Fprintln(stdout, "no migration applied")
		case st.Dirty:
			fmt.Fprintf(stdout, "version %d (dirty: the last run failed, fix it and use `migrate force` with the migrate CLI)\n", st.Version)
			return 1
		default:
			fmt.Fprintf(stdout, "version %d\n", st.Version)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown migrate command %q\n\n%s", args[0], usage)
		return 2
	}
}

// exitIfCommand runs a subcommand and exits; it returns only for "serve".
func exitIfCommand(cfg config.DoExamConfig) {
	if handled, code := runCommand(os.Args[1:], cfg, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
}

// runTLS handles `doexam tls init [host…]` (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md §7).
func runTLS(args []string, cfg config.DoExamConfig, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "init" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	hosts := args[1:]
	if len(hosts) == 0 {
		hosts = tlsutil.DefaultHosts()
	}
	res, err := tlsutil.Init(cfg.TLSDir, hosts, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if res.CACreated {
		fmt.Fprintf(stdout, "created a local CA: %s\n", res.CAPath)
	} else {
		fmt.Fprintf(stdout, "kept the existing CA: %s\n", res.CAPath)
	}
	cert, _ := filepath.Abs(res.CertPath)
	key, _ := filepath.Abs(res.KeyPath)
	fmt.Fprintf(stdout, `issued a certificate for %s (valid until %s)

Start the server with:
  TLS_CERT_FILE=%s
  TLS_KEY_FILE=%s

Install %s as a trusted root certificate on every lab PC (once), then open
https://<one of the hosts above>:<PORT>/exam/
CA fingerprint (SHA-256): %s
Keep ca.key private: anyone holding it can impersonate any site on those PCs.
`, strings.Join(res.Hosts, ", "), res.NotAfter.Format("2006-01-02"), cert, key, res.CAPath, res.CAFingerprint)
	return 0
}
