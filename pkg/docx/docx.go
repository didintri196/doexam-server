// Package docx reads the parts of a Word (.docx) document that matter for importing
// questions: paragraphs with basic formatting, Word list numbering, tables, embedded
// pictures and equations (OMML, converted to LaTeX for a common subset). It knows
// nothing about questions; see business/itembank for the import format.
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Limits protect the server from oversized or hostile files (zip bombs).
const (
	MaxPartBytes  = 20 << 20 // one unpacked XML part
	MaxMediaBytes = 10 << 20 // one unpacked picture
)

var (
	// ErrNotDocx means the file is not a readable Word document.
	ErrNotDocx = errors.New("not a .docx file")
	// ErrTooLarge means a part of the document exceeds the limits.
	ErrTooLarge = errors.New("document part too large")
)

const (
	nsW = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsM = "http://schemas.openxmlformats.org/officeDocument/2006/math"
	nsR = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

// Document is the content of word/document.xml in reading order.
type Document struct {
	Blocks []Block
	// Media holds the pictures referenced by runs, by relationship id.
	Media map[string]Image
	// Warnings lists things that were read only partly (unsupported equations, OLE objects).
	Warnings []string
}

// Image is an embedded picture.
type Image struct {
	Name string // file name inside the package, e.g. image1.png
	Data []byte
}

// Block is a paragraph or a table.
type Block struct {
	Para  *Para
	Table *Table
}

// Para is one paragraph.
type Para struct {
	Runs []Run
	// List numbering (Word "numbered list"); NumFmt is empty when the paragraph is not
	// in a list, else e.g. "decimal", "upperLetter", "lowerLetter", "bullet".
	NumID  string
	Level  int
	NumFmt string
}

// Run is a piece of text with one formatting, a picture, an equation or a line break.
type Run struct {
	Text                            string
	Bold, Italic, Underline, Strike bool
	Sub, Sup                        bool
	Image                           string // relationship id of a picture
	Math                            string // LaTeX of an equation
	Break                           bool
}

// Table is a table of paragraphs.
type Table struct {
	Rows [][]Cell
}

// Cell is one table cell.
type Cell struct {
	Paras []Para
}

// node is a generic XML element.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []node     `xml:",any"`
	Text     string     `xml:",chardata"`
}

func (n node) attr(local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (n node) child(space, local string) *node {
	for i := range n.Children {
		if n.Children[i].XMLName.Space == space && n.Children[i].XMLName.Local == local {
			return &n.Children[i]
		}
	}
	return nil
}

// Read parses a .docx file.
func Read(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotDocx
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	docFile := files["word/document.xml"]
	if docFile == nil {
		return nil, ErrNotDocx
	}
	var body node
	if err := readXML(docFile, &body); err != nil {
		return nil, err
	}

	rels := map[string]string{}
	if f := files["word/_rels/document.xml.rels"]; f != nil {
		var r node
		if err := readXML(f, &r); err != nil {
			return nil, err
		}
		for _, c := range r.Children {
			if c.XMLName.Local == "Relationship" {
				rels[c.attr("Id")] = c.attr("Target")
			}
		}
	}
	formats := map[string]map[int]string{} // numId → level → numFmt
	if f := files["word/numbering.xml"]; f != nil {
		var n node
		if err := readXML(f, &n); err != nil {
			return nil, err
		}
		formats = numberingFormats(n)
	}

	r := &reader{doc: &Document{Media: map[string]Image{}}, formats: formats}
	b := body.child(nsW, "body")
	if b == nil {
		return nil, ErrNotDocx
	}
	r.blocks(*b, &r.doc.Blocks)

	for id := range r.images {
		target, ok := rels[id]
		if !ok || strings.Contains(target, "://") {
			continue // linked (external) pictures are not imported
		}
		name := path.Clean(path.Join("word", target))
		f := files[name]
		if f == nil {
			continue
		}
		data, err := readAll(f, MaxMediaBytes)
		if err != nil {
			return nil, err
		}
		r.doc.Media[id] = Image{Name: path.Base(name), Data: data}
	}
	return r.doc, nil
}

func readAll(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, ErrTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrNotDocx
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, ErrNotDocx
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

func readXML(f *zip.File, into *node) error {
	data, err := readAll(f, MaxPartBytes)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(data, into); err != nil {
		return fmt.Errorf("%w: %s", ErrNotDocx, f.Name)
	}
	return nil
}

// numberingFormats resolves w:num → w:abstractNum → per-level w:numFmt.
func numberingFormats(n node) map[string]map[int]string {
	abstract := map[string]map[int]string{}
	for _, c := range n.Children {
		if c.XMLName.Local != "abstractNum" {
			continue
		}
		levels := map[int]string{}
		for _, l := range c.Children {
			if l.XMLName.Local != "lvl" {
				continue
			}
			ilvl, _ := strconv.Atoi(l.attr("ilvl"))
			if f := l.child(nsW, "numFmt"); f != nil {
				levels[ilvl] = f.attr("val")
			}
		}
		abstract[c.attr("abstractNumId")] = levels
	}
	out := map[string]map[int]string{}
	for _, c := range n.Children {
		if c.XMLName.Local != "num" {
			continue
		}
		if a := c.child(nsW, "abstractNumId"); a != nil {
			out[c.attr("numId")] = abstract[a.attr("val")]
		}
	}
	return out
}

type reader struct {
	doc     *Document
	formats map[string]map[int]string
	images  map[string]bool
}

func (r *reader) warn(msg string) {
	if !slices.Contains(r.doc.Warnings, msg) {
		r.doc.Warnings = append(r.doc.Warnings, msg)
	}
}

// blocks appends the paragraphs and tables of a container (body, cell, content control).
func (r *reader) blocks(n node, out *[]Block) {
	for _, c := range n.Children {
		if c.XMLName.Space != nsW {
			continue
		}
		switch c.XMLName.Local {
		case "p":
			p := r.para(c)
			*out = append(*out, Block{Para: &p})
		case "tbl":
			t := r.table(c)
			*out = append(*out, Block{Table: &t})
		case "sdt":
			if content := c.child(nsW, "sdtContent"); content != nil {
				r.blocks(*content, out)
			}
		}
	}
}

func (r *reader) table(n node) Table {
	var t Table
	for _, tr := range n.Children {
		if tr.XMLName.Local != "tr" {
			continue
		}
		var row []Cell
		for _, tc := range tr.Children {
			if tc.XMLName.Local != "tc" {
				continue
			}
			var blocks []Block
			r.blocks(tc, &blocks)
			var cell Cell
			for _, b := range blocks {
				if b.Para != nil {
					cell.Paras = append(cell.Paras, *b.Para)
				}
			}
			row = append(row, cell)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func (r *reader) para(n node) Para {
	var p Para
	if ppr := n.child(nsW, "pPr"); ppr != nil {
		if num := ppr.child(nsW, "numPr"); num != nil {
			if id := num.child(nsW, "numId"); id != nil {
				p.NumID = id.attr("val")
			}
			if lvl := num.child(nsW, "ilvl"); lvl != nil {
				p.Level, _ = strconv.Atoi(lvl.attr("val"))
			}
			if p.NumID != "" && p.NumID != "0" {
				p.NumFmt = r.formats[p.NumID][p.Level]
				if p.NumFmt == "" {
					p.NumFmt = "decimal"
				}
			}
		}
	}
	r.inline(n, &p.Runs)
	return p
}

// inline collects runs from a paragraph or an inline container (hyperlink, insertion…).
func (r *reader) inline(n node, out *[]Run) {
	for _, c := range n.Children {
		switch {
		case c.XMLName.Space == nsW && c.XMLName.Local == "r":
			r.run(c, out)
		case c.XMLName.Space == nsW && (c.XMLName.Local == "hyperlink" || c.XMLName.Local == "ins" ||
			c.XMLName.Local == "smartTag" || c.XMLName.Local == "fldSimple" || c.XMLName.Local == "customXml"):
			r.inline(c, out)
		case c.XMLName.Space == nsW && c.XMLName.Local == "sdt":
			if content := c.child(nsW, "sdtContent"); content != nil {
				r.inline(*content, out)
			}
		case c.XMLName.Space == nsM && (c.XMLName.Local == "oMath" || c.XMLName.Local == "oMathPara"):
			conv := &mathConv{}
			tex := strings.TrimSpace(conv.node(c))
			if conv.unsupported {
				r.warn("equation partly converted")
			}
			if tex != "" {
				*out = append(*out, Run{Math: tex})
			}
		}
	}
}

func (r *reader) run(n node, out *[]Run) {
	base := Run{}
	if rpr := n.child(nsW, "rPr"); rpr != nil {
		on := func(local string) bool {
			e := rpr.child(nsW, local)
			if e == nil {
				return false
			}
			v := e.attr("val")
			return v == "" || (v != "0" && v != "false" && v != "none")
		}
		base.Bold = on("b")
		base.Italic = on("i")
		base.Underline = on("u")
		base.Strike = on("strike") || on("dstrike")
		if va := rpr.child(nsW, "vertAlign"); va != nil {
			base.Sub = va.attr("val") == "subscript"
			base.Sup = va.attr("val") == "superscript"
		}
	}
	for _, c := range n.Children {
		switch c.XMLName.Local {
		case "t":
			t := base
			t.Text = c.Text
			*out = append(*out, t)
		case "tab":
			t := base
			t.Text = " "
			*out = append(*out, t)
		case "br", "cr":
			*out = append(*out, Run{Break: true})
		case "drawing", "pict":
			if id := findImage(c); id != "" {
				if r.images == nil {
					r.images = map[string]bool{}
				}
				r.images[id] = true
				*out = append(*out, Run{Image: id})
			}
		case "object":
			r.warn("embedded object (old equation editor or OLE) skipped")
		}
	}
}

// findImage returns the relationship id of the first picture inside a drawing or VML picture.
func findImage(n node) string {
	if n.XMLName.Local == "blip" || n.XMLName.Local == "imagedata" {
		for _, a := range n.Attrs {
			if a.Name.Space == nsR && (a.Name.Local == "embed" || a.Name.Local == "id") {
				return a.Value
			}
		}
	}
	for _, c := range n.Children {
		if id := findImage(c); id != "" {
			return id
		}
	}
	return ""
}

// Text returns the plain text of the paragraph (equations as their LaTeX).
func (p Para) Text() string {
	var b strings.Builder
	for _, r := range p.Runs {
		switch {
		case r.Math != "":
			b.WriteString(r.Math)
		case r.Break:
			b.WriteByte(' ')
		default:
			b.WriteString(r.Text)
		}
	}
	return b.String()
}

// HasContent reports whether the paragraph holds text, a picture or an equation.
func (p Para) HasContent() bool {
	for _, r := range p.Runs {
		if r.Image != "" || r.Math != "" || strings.TrimSpace(r.Text) != "" {
			return true
		}
	}
	return false
}

// AllBold reports whether every visible character is bold.
func (p Para) AllBold() bool {
	seen := false
	for _, r := range p.Runs {
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		if !r.Bold {
			return false
		}
		seen = true
	}
	return seen
}

// TrimPrefix drops the first n characters (runes) of text, e.g. a "1." or "A." label,
// and the spaces after it.
func (p Para) TrimPrefix(n int) Para {
	out := Para{NumID: p.NumID, Level: p.Level, NumFmt: p.NumFmt}
	trimSpace := true
	for _, r := range p.Runs {
		if n > 0 && r.Text != "" {
			rs := []rune(r.Text)
			if len(rs) <= n {
				n -= len(rs)
				continue
			}
			r.Text = string(rs[n:])
			n = 0
		}
		if trimSpace && r.Text != "" {
			r.Text = strings.TrimLeft(r.Text, " \t ")
			if r.Text == "" {
				continue
			}
		}
		if r.Text != "" || r.Image != "" || r.Math != "" {
			trimSpace = false
		}
		out.Runs = append(out.Runs, r)
	}
	return out
}
