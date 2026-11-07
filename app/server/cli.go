package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"doexam/config"
	"doexam/migrations"
	"doexam/pkg/dbmigrate"
)

const usage = `DoExam server

Usage:
  doexam [serve]            run the server (default)
  doexam migrate up         apply pending database migrations
  doexam migrate down N     roll back the last N migrations
  doexam migrate version    show the current schema version
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
