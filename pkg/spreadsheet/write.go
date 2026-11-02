package spreadsheet

import (
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"
)

// Column describes one template column.
type Column struct {
	Name     string // header, e.g. "username"
	Required bool   // header gets a "*" and a highlight
	Note     string // explanation on the instructions sheet
}

// Template describes a downloadable import template.
type Template struct {
	Columns      []Column
	Examples     [][]string // example data rows
	Instructions []string   // extra lines on the instructions sheet
}

// WriteTemplate writes an .xlsx with a "Data" sheet (headers + examples, every
// cell formatted as text so leading zeros such as NIS survive) and a "Petunjuk" sheet.
func WriteTemplate(w io.Writer, t Template) error {
	f := excelize.NewFile()
	defer f.Close()

	if err := f.SetSheetName(f.GetSheetName(0), DataSheet); err != nil {
		return err
	}
	text, err := f.NewStyle(&excelize.Style{NumFmt: 49}) // "@" = text
	if err != nil {
		return err
	}
	head, err := f.NewStyle(&excelize.Style{NumFmt: 49, Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	req, err := f.NewStyle(&excelize.Style{
		NumFmt: 49, Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFF2CC"}},
	})
	if err != nil {
		return err
	}

	last, _ := excelize.ColumnNumberToName(len(t.Columns))
	if err := f.SetColStyle(DataSheet, "A:"+last, text); err != nil {
		return err
	}
	if err := f.SetColWidth(DataSheet, "A", last, 22); err != nil {
		return err
	}
	for i, c := range t.Columns {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		name, style := c.Name, head
		if c.Required {
			name, style = c.Name+"*", req
		}
		if err := f.SetCellStr(DataSheet, cell, name); err != nil {
			return err
		}
		if err := f.SetCellStyle(DataSheet, cell, cell, style); err != nil {
			return err
		}
	}
	for r, row := range t.Examples {
		if err := writeRow(f, DataSheet, r+2, row); err != nil {
			return err
		}
	}
	if err := f.SetPanes(DataSheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}

	const help = "Petunjuk"
	if _, err := f.NewSheet(help); err != nil {
		return err
	}
	_ = f.SetColWidth(help, "A", "A", 24)
	_ = f.SetColWidth(help, "B", "B", 90)
	line := 1
	for _, s := range t.Instructions {
		_ = f.SetCellStr(help, fmt.Sprintf("A%d", line), s)
		line++
	}
	line++
	_ = f.SetCellStr(help, fmt.Sprintf("A%d", line), "Kolom")
	_ = f.SetCellStr(help, fmt.Sprintf("B%d", line), "Keterangan")
	_ = f.SetCellStyle(help, fmt.Sprintf("A%d", line), fmt.Sprintf("B%d", line), head)
	for _, c := range t.Columns {
		line++
		name := c.Name
		if c.Required {
			name += " (wajib)"
		}
		_ = f.SetCellStr(help, fmt.Sprintf("A%d", line), name)
		_ = f.SetCellStr(help, fmt.Sprintf("B%d", line), c.Note)
	}
	return f.Write(w)
}

// WriteTable writes a single-sheet .xlsx with a bold header row. All cells are text.
func WriteTable(w io.Writer, sheet string, headers []string, rows [][]string) error {
	f := excelize.NewFile()
	defer f.Close()

	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return err
	}
	text, err := f.NewStyle(&excelize.Style{NumFmt: 49})
	if err != nil {
		return err
	}
	head, err := f.NewStyle(&excelize.Style{NumFmt: 49, Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	last, _ := excelize.ColumnNumberToName(max(len(headers), 1))
	_ = f.SetColStyle(sheet, "A:"+last, text)
	_ = f.SetColWidth(sheet, "A", last, 22)
	if err := writeRow(f, sheet, 1, headers); err != nil {
		return err
	}
	_ = f.SetCellStyle(sheet, "A1", last+"1", head)
	for r, row := range rows {
		if err := writeRow(f, sheet, r+2, row); err != nil {
			return err
		}
	}
	return f.Write(w)
}

func writeRow(f *excelize.File, sheet string, row int, values []string) error {
	for i, v := range values {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		if err := f.SetCellStr(sheet, cell, v); err != nil {
			return err
		}
	}
	return nil
}
