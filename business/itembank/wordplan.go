package itembank

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"doexam/pkg/docx"
)

// Word import format (the downloadable template explains it to authors):
//
//	Jenis: pg | pgk | bs | isian | esai     applies to the items that follow
//	Bacaan: <judul>                          reading text for the items that follow…
//	…paragraphs, pictures, tables…
//	Akhir bacaan                             …until here (or the next "Bacaan:")
//	1. Question text (more paragraphs, pictures and tables may follow)
//	A. option   *B. correct option   C. …   (or Word's automatic numbering/lettering)
//	Kunci: B | A, C | Benar | jawaban1 | jawaban2
//	Skor: 2
//	Kompetensi: KD-3.1
//	Pembahasan: explanation (more paragraphs may follow)
var (
	reItemNo   = regexp.MustCompile(`^\s*(\d{1,3})\s*[.)]\s*`)
	reOption   = regexp.MustCompile(`^\s*(\*?)\s*([A-Ja-j])\s*[.)]\s+`)
	reLabel    = regexp.MustCompile(`(?i)^\s*(jenis|kunci|jawaban|skor|poin|kompetensi|pembahasan)\s*:\s*`)
	reBacaan   = regexp.MustCompile(`(?i)^\s*(bacaan|stimulus)\s*(:\s*|$)`)
	reEndRead  = regexp.MustCompile(`(?i)^\s*akhir\s+(bacaan|stimulus)\s*\.?\s*$`)
	typeByWord = map[string]string{
		"pg": "mcq_single", "pilihan ganda": "mcq_single",
		"pgk": "mcq_multi", "pg kompleks": "mcq_multi", "pilihan ganda kompleks": "mcq_multi",
		"bs": "true_false", "benar salah": "true_false", "benar/salah": "true_false",
		"isian": "short_answer", "isian singkat": "short_answer",
		"esai": "essay", "essay": "essay", "uraian": "essay",
	}
)

// wordOption is an answer option as written.
type wordOption struct {
	label  string // letter as written, upper case ("" with Word automatic lettering)
	body   string // inline HTML
	marked bool   // "*" before the letter
	bold   bool   // the whole option is bold
}

// wordItem is an item as written, before validation.
type wordItem struct {
	no          int
	typ         string // "" = decide from the content
	stem        []string
	options     []wordOption
	key         string
	points      string
	competency  string
	explanation []string
	stimulus    int // index into plan.stimuli, -1 = none
	issues      []string
}

type wordStimulus struct {
	title string
	body  []string
}

type wordPlan struct {
	items    []*wordItem
	stimuli  []*wordStimulus
	warnings []string
	images   map[string]string // relationship id → temporary media id used in the HTML
}

// tempMediaID is a placeholder id for a picture until it is stored.
func tempMediaID(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }

// planWord turns a document into items. It never fails: problems are attached to the
// item they concern (or to the document) so the author sees all of them at once.
func planWord(d *docx.Document) *wordPlan {
	pl := &wordPlan{images: map[string]string{}}
	img := func(rid string) string {
		if _, ok := d.Media[rid]; !ok {
			return ""
		}
		id, ok := pl.images[rid]
		if !ok {
			id = tempMediaID(len(pl.images) + 1)
			pl.images[rid] = id
		}
		return `<img data-media-id="` + id + `" alt="">`
	}

	var (
		cur     *wordItem
		curType string
		reading = -1 // stimulus that new items get
		target  = "" // where continuation paragraphs go: stem, option, explanation, stimulus
	)
	appendHTML := func(html string, block bool) {
		if html == "" {
			return
		}
		wrap := func() string {
			if block && !strings.HasPrefix(html, "<table") {
				return "<p>" + html + "</p>"
			}
			return html
		}
		switch target {
		case "stem":
			cur.stem = append(cur.stem, wrap())
		case "option":
			o := &cur.options[len(cur.options)-1]
			if strings.HasPrefix(html, "<table") {
				o.body += html
			} else {
				o.body += "<br>" + html
			}
		case "explanation":
			cur.explanation = append(cur.explanation, wrap())
		case "stimulus":
			pl.stimuli[reading].body = append(pl.stimuli[reading].body, wrap())
		}
	}
	finish := func() {
		if cur != nil {
			pl.items = append(pl.items, cur)
			cur = nil
		}
	}

	for _, b := range d.Blocks {
		if b.Table != nil {
			if target == "" {
				pl.warnings = append(pl.warnings, "table outside an item or reading text skipped")
				continue
			}
			appendHTML(b.Table.HTML(img), true)
			continue
		}
		p := *b.Para
		if !p.HasContent() {
			continue
		}
		text := p.Text()

		switch {
		case reEndRead.MatchString(text):
			finish()
			reading, target = -1, ""
			continue
		case reBacaan.MatchString(text) && p.NumFmt == "":
			finish()
			m := reBacaan.FindString(text)
			title := strings.TrimSpace(p.TrimPrefix(utf8.RuneCountInString(m)).Text())
			if title == "" {
				title = fmt.Sprintf("Bacaan %d", len(pl.stimuli)+1)
			}
			pl.stimuli = append(pl.stimuli, &wordStimulus{title: truncateRunes(title, 200)})
			reading, target = len(pl.stimuli)-1, "stimulus"
			continue
		}

		if m := reLabel.FindStringSubmatch(text); m != nil && p.NumFmt == "" {
			label := strings.ToLower(m[1])
			rest := p.TrimPrefix(utf8.RuneCountInString(m[0]))
			if label == "jenis" {
				w := strings.ToLower(strings.TrimSpace(rest.Text()))
				if t, ok := typeByWord[w]; ok {
					curType = t
				} else {
					pl.warnings = append(pl.warnings, fmt.Sprintf("unknown item type %q (use pg, pgk, bs, isian or esai)", w))
				}
				target = ""
				continue
			}
			if cur == nil {
				pl.warnings = append(pl.warnings, fmt.Sprintf("%q before the first item skipped", m[1]))
				continue
			}
			switch label {
			case "kunci", "jawaban":
				cur.key, target = strings.TrimSpace(rest.Text()), ""
			case "skor", "poin":
				cur.points, target = strings.TrimSpace(rest.Text()), ""
			case "kompetensi":
				cur.competency, target = strings.TrimSpace(rest.Text()), ""
			case "pembahasan":
				target = "explanation"
				if rest.HasContent() {
					cur.explanation = append(cur.explanation, "<p>"+rest.HTML(img)+"</p>")
				}
			}
			continue
		}

		// a new item: "12." / "12)" or Word's automatic decimal numbering at the top level
		if m := reItemNo.FindString(text); m != "" || isItemList(p) {
			finish()
			cur = &wordItem{no: len(pl.items) + 1, typ: curType, stimulus: reading}
			body := p
			if m != "" {
				body = p.TrimPrefix(utf8.RuneCountInString(m))
			}
			target = "stem"
			if body.HasContent() {
				cur.stem = append(cur.stem, "<p>"+body.HTML(img)+"</p>")
			}
			continue
		}

		// an option of the current item: "A." / "*B)" or Word's automatic lettering
		if cur != nil && (target == "stem" || target == "option") {
			if m := reOption.FindStringSubmatch(text); m != nil || isOptionList(p) {
				o := wordOption{}
				body := p
				if m != nil {
					o.marked, o.label = m[1] == "*", strings.ToUpper(m[2])
					body = p.TrimPrefix(utf8.RuneCountInString(m[0]))
				} else if strings.HasPrefix(strings.TrimSpace(text), "*") {
					o.marked = true
					body = p.TrimPrefix(utf8.RuneCountInString(text) - utf8.RuneCountInString(strings.TrimLeft(text, " \t*")))
				}
				o.bold = body.AllBold()
				o.body = body.HTML(img)
				cur.options = append(cur.options, o)
				target = "option"
				continue
			}
		}

		if target == "" {
			if cur == nil && reading < 0 {
				pl.warnings = append(pl.warnings, "text before the first item skipped")
			}
			continue
		}
		appendHTML(p.HTML(img), true)
	}
	finish()
	return pl
}

func isItemList(p docx.Para) bool {
	return p.Level == 0 && (p.NumFmt == "decimal" || p.NumFmt == "decimalZero")
}

func isOptionList(p docx.Para) bool {
	switch p.NumFmt {
	case "upperLetter", "lowerLetter":
		return true
	case "decimal", "decimalZero":
		return p.Level > 0
	}
	return false
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
