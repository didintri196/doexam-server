// Package spreadsheet reads tabular uploads (.xlsx, .csv) and writes .xlsx files.
// It knows nothing about the domain: callers map header names to fields.
package spreadsheet

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Format is a supported file format.
type Format string

const (
	FormatXLSX Format = "xlsx"
	FormatCSV  Format = "csv"
)

// DataSheet is the sheet name read from (and written to) workbooks.
// When absent, the first sheet is read.
const DataSheet = "Data"

var (
	// ErrUnsupportedFormat is returned for anything but .xlsx and .csv.
	ErrUnsupportedFormat = errors.New("unsupported file format, use .xlsx or .csv")
	// ErrNoHeader is returned when the file has no header row.
	ErrNoHeader = errors.New("file has no header row")
	// ErrTooManyRows is returned when the file has more data rows than allowed.
	ErrTooManyRows = errors.New("file has too many rows")
)

// Row is one non-empty data row. Line is the 1-based line number as the user sees it.
type Row struct {
	Line   int
	Values map[string]string // header → trimmed cell value
}

// Get returns the trimmed value of column name ("" when absent).
func (r Row) Get(name string) string { return r.Values[name] }

// Sheet is the parsed content of an upload.
type Sheet struct {
	Headers []string
	Rows    []Row
}

// DetectFormat returns the format of data, using the file name as a hint.
func DetectFormat(filename string, data []byte) (Format, error) {
	name := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(name, ".xlsx") && bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return FormatXLSX, nil
	case strings.HasSuffix(name, ".csv") && utf8.Valid(data):
		return FormatCSV, nil
	}
	return "", ErrUnsupportedFormat
}

// Read parses data. The first non-empty row is the header; header names are
// normalised with NormalizeHeader. Fully empty rows are skipped. More than
// maxRows data rows returns ErrTooManyRows.
func Read(data []byte, format Format, maxRows int) (Sheet, error) {
	var (
		records [][]string
		lines   []int // line number of each record as the user sees it
		err     error
	)
	switch format {
	case FormatXLSX:
		records, err = readXLSX(data)
		lines = make([]int, len(records))
		for i := range lines {
			lines[i] = i + 1 // GetRows keeps empty rows in between, so index = row - 1
		}
	case FormatCSV:
		records, lines, err = readCSV(data)
	default:
		return Sheet{}, ErrUnsupportedFormat
	}
	if err != nil {
		return Sheet{}, err
	}

	var sheet Sheet
	for i, rec := range records {
		if isEmpty(rec) {
			continue
		}
		if sheet.Headers == nil {
			sheet.Headers = make([]string, len(rec))
			for j, h := range rec {
				sheet.Headers[j] = NormalizeHeader(h)
			}
			continue
		}
		if len(sheet.Rows) == maxRows {
			return Sheet{}, fmt.Errorf("%w (max %d)", ErrTooManyRows, maxRows)
		}
		row := Row{Line: lines[i], Values: make(map[string]string, len(sheet.Headers))}
		for j, h := range sheet.Headers {
			if h != "" && j < len(rec) {
				row.Values[h] = strings.TrimSpace(rec[j])
			}
		}
		sheet.Rows = append(sheet.Rows, row)
	}
	if sheet.Headers == nil {
		return Sheet{}, ErrNoHeader
	}
	return sheet, nil
}

// NormalizeHeader lowercases h, trims it, drops a trailing "*" (required marker
// in templates) and turns spaces/dashes into underscores: " Full Name* " → "full_name".
func NormalizeHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\xEF\xBB\xBF")))
	h = strings.TrimSpace(strings.TrimSuffix(h, "*"))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(h)
}

func readXLSX(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot open workbook", ErrUnsupportedFormat)
	}
	defer f.Close()

	sheet := DataSheet
	if idx, _ := f.GetSheetIndex(DataSheet); idx < 0 {
		sheet = f.GetSheetName(0)
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("read sheet %q: %w", sheet, err)
	}
	return rows, nil
}

func readCSV(data []byte) ([][]string, []int, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = detectDelimiter(data)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var (
		out   [][]string
		lines []int
	)
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, lines, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid CSV: %v", ErrUnsupportedFormat, err)
		}
		line, _ := r.FieldPos(0)
		out = append(out, rec)
		lines = append(lines, line)
	}
}

// detectDelimiter picks ';' when the header line has more of them than ','
// (Excel with Indonesian locale exports CSV with ';').
func detectDelimiter(data []byte) rune {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	if bytes.Count(line, []byte(";")) > bytes.Count(line, []byte(",")) {
		return ';'
	}
	return ','
}

func isEmpty(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}
