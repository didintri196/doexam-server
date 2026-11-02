package spreadsheet

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadCSV(t *testing.T) {
	data := []byte("\xEF\xBB\xBFUsername*;Full Name;Email\n\n0051234567;Budi; b@x.id \n;;\nsiswa2;Siti;\n")
	f, err := DetectFormat("data.CSV", data)
	if err != nil || f != FormatCSV {
		t.Fatalf("DetectFormat = %v, %v", f, err)
	}
	s, err := Read(data, f, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Headers; len(got) != 3 || got[0] != "username" || got[1] != "full_name" {
		t.Fatalf("headers = %v", got)
	}
	if len(s.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (empty rows skipped)", len(s.Rows))
	}
	if r := s.Rows[0]; r.Line != 3 || r.Get("username") != "0051234567" || r.Get("email") != "b@x.id" {
		t.Fatalf("row 0 = %+v", r)
	}
	if s.Rows[1].Line != 5 {
		t.Fatalf("line of row 1 = %d, want 5", s.Rows[1].Line)
	}
}

func TestReadLimitsAndErrors(t *testing.T) {
	if _, err := Read([]byte("a\n1\n2\n3\n"), FormatCSV, 2); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("err = %v, want ErrTooManyRows", err)
	}
	if _, err := Read([]byte("\n\n"), FormatCSV, 2); !errors.Is(err, ErrNoHeader) {
		t.Fatalf("err = %v, want ErrNoHeader", err)
	}
	if _, err := DetectFormat("x.xlsx", []byte("not a zip")); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := DetectFormat("x.xls", []byte("PK\x03\x04")); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestTemplateRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	err := WriteTemplate(&buf, Template{
		Columns:      []Column{{Name: "username", Required: true, Note: "NIS"}, {Name: "full_name", Required: true}},
		Examples:     [][]string{{"0051234567", "Budi"}},
		Instructions: []string{"Isi sheet Data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := DetectFormat("t.xlsx", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Read(buf.Bytes(), f, 10)
	if err != nil {
		t.Fatal(err)
	}
	if s.Headers[0] != "username" || len(s.Rows) != 1 || s.Rows[0].Get("username") != "0051234567" {
		t.Fatalf("round trip = %+v", s)
	}
}

func TestWriteTable(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTable(&buf, "Akun", []string{"username", "password"}, [][]string{{"007", "abc"}}); err != nil {
		t.Fatal(err)
	}
	s, err := Read(buf.Bytes(), FormatXLSX, 10)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rows[0].Get("username") != "007" {
		t.Fatalf("leading zeros lost: %+v", s.Rows[0])
	}
}
