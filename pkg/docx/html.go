package docx

import (
	"html"
	"strings"
)

// HTML renders the runs of the paragraph as inline HTML within the item allowlist
// (strong, em, u, s, sub, sup, br, span data-math). img returns the markup for a picture
// by relationship id ("" to drop it). The result is meant to be sanitized again.
func (p Para) HTML(img func(rid string) string) string {
	var b strings.Builder
	for _, r := range p.Runs {
		switch {
		case r.Break:
			b.WriteString("<br>")
		case r.Image != "":
			b.WriteString(img(r.Image))
		case r.Math != "":
			b.WriteString(`<span data-math="` + html.EscapeString(r.Math) + `">` + html.EscapeString(r.Math) + `</span>`)
		case r.Text != "":
			open, closeTags := "", ""
			for _, t := range []struct {
				on  bool
				tag string
			}{{r.Bold, "strong"}, {r.Italic, "em"}, {r.Underline, "u"}, {r.Strike, "s"}, {r.Sub, "sub"}, {r.Sup, "sup"}} {
				if t.on {
					open += "<" + t.tag + ">"
					closeTags = "</" + t.tag + ">" + closeTags
				}
			}
			b.WriteString(open + html.EscapeString(r.Text) + closeTags)
		}
	}
	return b.String()
}

// HTML renders the table; paragraphs in a cell are separated by line breaks.
func (t Table) HTML(img func(rid string) string) string {
	var b strings.Builder
	b.WriteString("<table><tbody>")
	for _, row := range t.Rows {
		b.WriteString("<tr>")
		for _, c := range row {
			parts := make([]string, 0, len(c.Paras))
			for _, p := range c.Paras {
				parts = append(parts, p.HTML(img))
			}
			b.WriteString("<td>" + strings.Join(parts, "<br>") + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}
