package docx

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
	`xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" ` +
	`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
	`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`

func build(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestRead(t *testing.T) {
	doc := `<w:document ` + wNS + `><w:body>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>Hitung </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>luas</w:t></w:r><w:r><w:t xml:space="preserve"> dari </w:t></w:r>
  <m:oMath><m:f><m:num><m:r><m:t>a</m:t></m:r></m:num><m:den><m:r><m:t>2</m:t></m:r></m:den></m:f><m:r><m:t>×</m:t></m:r><m:sSup><m:e><m:r><m:t>x</m:t></m:r></m:e><m:sup><m:r><m:t>2</m:t></m:r></m:sup></m:sSup></m:oMath></w:p>
<w:p><w:r><w:drawing><a:blip r:embed="rId5"/></w:drawing></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:rPr><w:vertAlign w:val="subscript"/></w:rPr><w:t>2</w:t></w:r><w:r><w:rPr><w:b w:val="0"/></w:rPr><w:t>H&amp;O</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>x</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>y</w:t></w:r></w:p><w:p><w:r><w:t>z</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><m:oMathPara><m:oMath><m:rad><m:radPr><m:degHide m:val="1"/></m:radPr><m:deg/><m:e><m:r><m:t>16</m:t></m:r></m:e></m:rad><m:m><m:mr><m:e><m:r><m:t>1</m:t></m:r></m:e></m:mr></m:m></m:oMath></m:oMathPara></w:p>
<w:p><w:r><w:object/></w:r></w:p>
</w:body></w:document>`
	numbering := `<w:numbering ` + wNS + `>
<w:abstractNum w:abstractNumId="7"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl><w:lvl w:ilvl="1"><w:numFmt w:val="upperLetter"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="7"/></w:num></w:numbering>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId5" Type="image" Target="media/image1.png"/><Relationship Id="rId9" Type="hyperlink" Target="https://x" TargetMode="External"/></Relationships>`
	data := build(t, map[string]string{
		"word/document.xml": doc, "word/numbering.xml": numbering, "word/_rels/document.xml.rels": rels,
		"word/media/image1.png": "PNGDATA",
	})

	d, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 6 {
		t.Fatalf("blocks = %d, want 6", len(d.Blocks))
	}
	p0 := d.Blocks[0].Para
	if p0.NumFmt != "decimal" || p0.Level != 0 {
		t.Fatalf("numbering = %q/%d", p0.NumFmt, p0.Level)
	}
	img := func(rid string) string { return "[img:" + rid + "]" }
	if got := p0.HTML(img); got != `Hitung <strong>luas</strong> dari <span data-math="\frac{a}{2}\times x^2">\frac{a}{2}\times x^2</span>` {
		t.Fatalf("p0 html = %q", got)
	}
	if got := d.Blocks[1].Para.HTML(img); got != "[img:rId5]" || string(d.Media["rId5"].Data) != "PNGDATA" {
		t.Fatalf("image: %q media=%v", got, d.Media)
	}
	p2 := d.Blocks[2].Para
	if p2.NumFmt != "upperLetter" || p2.HTML(img) != "<sub>2</sub>H&amp;O" {
		t.Fatalf("p2 = %q %q", p2.NumFmt, p2.HTML(img))
	}
	if got := d.Blocks[3].Table.HTML(img); got != "<table><tbody><tr><td>x</td><td>y<br>z</td></tr></tbody></table>" {
		t.Fatalf("table = %q", got)
	}
	if got := d.Blocks[4].Para.Runs[0].Math; !strings.HasPrefix(got, `\sqrt{16}`) {
		t.Fatalf("sqrt = %q", got)
	}
	if strings.Join(d.Warnings, "|") != "equation partly converted|embedded object (old equation editor or OLE) skipped" {
		t.Fatalf("warnings = %v", d.Warnings)
	}
}

func TestTrimPrefixAndBold(t *testing.T) {
	p := Para{Runs: []Run{{Text: "*B"}, {Text: ". ", Bold: true}, {Text: " Jakarta", Bold: true}}}
	got := p.TrimPrefix(3)
	if got.Text() != "Jakarta" || !got.AllBold() || p.AllBold() {
		t.Fatalf("trim = %q bold=%v orig bold=%v", got.Text(), got.AllBold(), p.AllBold())
	}
	if (Para{Runs: []Run{{Image: "r1"}}}).AllBold() {
		t.Fatal("a picture alone is not bold text")
	}
}

func TestMath(t *testing.T) {
	cases := map[string]string{
		`<m:d><m:e><m:r><m:t>a+b</m:t></m:r></m:e></m:d>`: `\left(a+b\right)`,
		`<m:nary><m:naryPr><m:chr m:val="∑"/></m:naryPr><m:sub><m:r><m:t>i=1</m:t></m:r></m:sub><m:sup><m:r><m:t>n</m:t></m:r></m:sup><m:e><m:r><m:t>i</m:t></m:r></m:e></m:nary>`: `\sum_{i=1}^n i`,
		`<m:rad><m:deg><m:r><m:t>3</m:t></m:r></m:deg><m:e><m:r><m:t>8</m:t></m:r></m:e></m:rad>`:                                                                                  `\sqrt[3]{8}`,
		`<m:sSub><m:e><m:r><m:t>x</m:t></m:r></m:e><m:sub><m:r><m:t>12</m:t></m:r></m:sub></m:sSub>`:                                                                               `x_{12}`,
		`<m:func><m:fName><m:r><m:t>sin</m:t></m:r></m:fName><m:e><m:r><m:t>θ</m:t></m:r></m:e></m:func>`:                                                                          `\sin {\theta}`,
	}
	for in, want := range cases {
		doc := `<w:document ` + wNS + `><w:body><w:p><m:oMath>` + in + `</m:oMath></w:p></w:body></w:document>`
		d, err := Read(build(t, map[string]string{"word/document.xml": doc}))
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Blocks[0].Para.Runs[0].Math; got != want {
			t.Errorf("%s\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestReadRejects(t *testing.T) {
	if _, err := Read([]byte("not a zip")); !errors.Is(err, ErrNotDocx) {
		t.Fatalf("plain text: %v", err)
	}
	if _, err := Read(build(t, map[string]string{"x.xml": "<a/>"})); !errors.Is(err, ErrNotDocx) {
		t.Fatalf("zip without document: %v", err)
	}
	if _, err := Read(build(t, map[string]string{"word/document.xml": "<w:document"})); !errors.Is(err, ErrNotDocx) {
		t.Fatalf("broken xml: %v", err)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, []Line{"1. Soal **tebal** & <aman>", "", "A. opsi"}); err != nil {
		t.Fatal(err)
	}
	d, err := Read(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 3 || d.Blocks[0].Para.HTML(nil) != "1. Soal <strong>tebal</strong> &amp; &lt;aman&gt;" {
		t.Fatalf("round trip = %q", d.Blocks[0].Para.HTML(nil))
	}
}
