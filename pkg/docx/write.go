package docx

import (
	"archive/zip"
	"html"
	"io"
	"strings"
)

// Line is one paragraph of a generated document: text with **bold** spans.
type Line string

// Write produces a minimal .docx with one paragraph per line (used for templates).
// "**x**" makes x bold; an empty line is an empty paragraph.
func Write(w io.Writer, lines []Line) error {
	zw := zip.NewWriter(w)
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`,
		"word/document.xml": document(lines),
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, parts[name]); err != nil {
			return err
		}
	}
	return zw.Close()
}

func document(lines []Line) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, l := range lines {
		b.WriteString("<w:p>")
		bold := false
		for _, seg := range strings.Split(string(l), "**") {
			if seg != "" {
				b.WriteString("<w:r>")
				if bold {
					b.WriteString("<w:rPr><w:b/></w:rPr>")
				}
				b.WriteString(`<w:t xml:space="preserve">` + html.EscapeString(seg) + "</w:t></w:r>")
			}
			bold = !bold
		}
		b.WriteString("</w:p>")
	}
	b.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr></w:body></w:document>`)
	return b.String()
}
