package itembank

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	modelMedia "doexam/models/media"
	"doexam/pkg/docx"
	"doexam/pkg/errr"
)

func wordDoc(t *testing.T, lines ...docx.Line) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := docx.Write(&buf, lines); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readPlan(t *testing.T, data []byte) *wordPlan {
	t.Helper()
	d, err := docx.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	return planWord(d)
}

func TestPlanWordTemplate(t *testing.T) {
	data, err := WordTemplate()
	if err != nil {
		t.Fatal(err)
	}
	pl := readPlan(t, data)
	if len(pl.items) != 6 || len(pl.stimuli) != 1 {
		t.Fatalf("items = %d, stimuli = %d, want 6 and 1", len(pl.items), len(pl.stimuli))
	}
	types := []string{}
	for _, it := range pl.items {
		b := buildWordItem(it, pl, map[string]string{}, map[string]bool{})
		if len(b.dto.Errors) > 0 {
			t.Errorf("template item %d invalid: %+v", it.no, b.dto.Errors)
		}
		types = append(types, string(b.dto.Type))
	}
	want := "mcq_single mcq_multi true_false short_answer essay mcq_single"
	if strings.Join(types, " ") != want {
		t.Fatalf("types = %v, want %s", types, want)
	}
	if pl.items[5].stimulus != 0 || pl.items[4].stimulus != -1 {
		t.Fatal("only the item after Bacaan uses the reading text")
	}
	b := buildWordItem(pl.items[0], pl, nil, nil)
	if b.dto.Options[1].Correct != true || *b.dto.Points != 1 || !strings.Contains(b.dto.Explanation, "Bandung") {
		t.Fatalf("item 1 = %+v", b.dto)
	}
}

func TestBuildWordItemRules(t *testing.T) {
	cases := []struct {
		name     string
		lines    []docx.Line
		wantType modelBank.ItemType
		wantErr  string // substring of the first error message, "" = valid
		check    func(t *testing.T, b builtItem)
	}{
		{name: "bold option is the key when none is given", lines: []docx.Line{"1. Soal", "A. satu", "**B. dua**", "C. tiga"}, wantType: modelBank.TypeMCQSingle,
			check: func(t *testing.T, b builtItem) {
				if !b.dto.Options[1].Correct {
					t.Fatal("bold option B must be correct")
				}
			}},
		{name: "two letters make it multiple choice complex", lines: []docx.Line{"1) Soal", "a) x", "b) y", "c) z", "Kunci: a dan c"}, wantType: modelBank.TypeMCQMulti,
			check: func(t *testing.T, b builtItem) {
				var k struct{ Correct []string }
				json.Unmarshal(b.input.AnswerKey, &k)
				if strings.Join(k.Correct, ",") != "a,c" {
					t.Fatalf("key = %s", b.input.AnswerKey)
				}
			}},
		{name: "key letter without option", lines: []docx.Line{"1. Soal", "A. x", "B. y", "Kunci: E"}, wantType: modelBank.TypeMCQSingle, wantErr: "does not match an option"},
		{name: "missing key", lines: []docx.Line{"1. Soal", "A. x", "B. y"}, wantType: modelBank.TypeMCQSingle, wantErr: "answer key missing"},
		{name: "one option only", lines: []docx.Line{"1. Soal", "*A. x"}, wantType: modelBank.TypeMCQSingle, wantErr: "2-10 options"},
		{name: "declared single with two keys", lines: []docx.Line{"Jenis: pg", "1. Soal", "A. x", "B. y", "Kunci: A, B"}, wantType: modelBank.TypeMCQSingle, wantErr: "exactly one answer"},
		{name: "numeric short answer", lines: []docx.Line{"1. 2+2?", "Kunci: 4 | 4,0"}, wantType: modelBank.TypeShortAnswer,
			check: func(t *testing.T, b builtItem) {
				if !strings.Contains(string(b.input.Content), `"input":"number"`) {
					t.Fatalf("content = %s", b.input.Content)
				}
			}},
		{name: "true false", lines: []docx.Line{"Jenis: bs", "1. Bumi bulat", "Kunci: B"}, wantType: modelBank.TypeTrueFalse},
		{name: "true false bad key", lines: []docx.Line{"Jenis: bs", "1. Bumi bulat", "Kunci: mungkin"}, wantType: modelBank.TypeTrueFalse, wantErr: "Benar or Salah"},
		{name: "essay with model answer", lines: []docx.Line{"1. Jelaskan <x>", "Kunci: a & b"}, wantType: modelBank.TypeShortAnswer},
		{name: "essay", lines: []docx.Line{"1. Jelaskan", "paragraf kedua"}, wantType: modelBank.TypeEssay,
			check: func(t *testing.T, b builtItem) {
				if !strings.Contains(b.dto.Stem, "paragraf kedua") {
					t.Fatalf("stem = %q", b.dto.Stem)
				}
			}},
		{name: "bad score", lines: []docx.Line{"1. Jelaskan", "Skor: banyak"}, wantType: modelBank.TypeEssay, wantErr: "score must be"},
		{name: "unknown competency", lines: []docx.Line{"1. Jelaskan", "Kompetensi: KD-9"}, wantType: modelBank.TypeEssay, wantErr: "not found"},
		{name: "known competency", lines: []docx.Line{"1. Jelaskan", "Kompetensi: kd-3.1"}, wantType: modelBank.TypeEssay,
			check: func(t *testing.T, b builtItem) {
				if b.input.CompetencyID == nil || *b.input.CompetencyID != "comp-1" {
					t.Fatal("competency code must resolve")
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pl := readPlan(t, wordDoc(t, tc.lines...))
			if len(pl.items) != 1 {
				t.Fatalf("items = %d", len(pl.items))
			}
			b := buildWordItem(pl.items[0], pl, map[string]string{"kd-3.1": "comp-1"}, nil)
			if b.dto.Type != tc.wantType {
				t.Fatalf("type = %s, want %s", b.dto.Type, tc.wantType)
			}
			switch {
			case tc.wantErr == "" && len(b.dto.Errors) > 0:
				t.Fatalf("errors = %+v", b.dto.Errors)
			case tc.wantErr != "" && (len(b.dto.Errors) == 0 || !strings.Contains(b.dto.Errors[0].Message, tc.wantErr)):
				t.Fatalf("errors = %+v, want %q", b.dto.Errors, tc.wantErr)
			}
			if tc.check != nil {
				tc.check(t, b)
			}
		})
	}
}

// Word's own numbering: items as a decimal list, options as a lettered list, no typed labels.
func TestPlanWordAutoNumbering(t *testing.T) {
	ns := `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`
	li := func(num, text string) string {
		return `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="` + num + `"/></w:numPr></w:pPr><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
	}
	doc := `<w:document ` + ns + `><w:body>` + li("1", "Gambar berikut menunjukkan apa?") +
		`<w:p><w:r><w:drawing><a:blip r:embed="rId1"/></w:drawing></w:r></w:p>` +
		li("2", "Kucing") + li("2", "*Anjing") + li("2", "Ikan") +
		`<w:p><w:r><w:t>Pembahasan: lihat telinganya.</w:t></w:r></w:p></w:body></w:document>`
	numbering := `<w:numbering ` + ns + `><w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>` +
		`<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="upperLetter"/></w:lvl></w:abstractNum>` +
		`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num><w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num></w:numbering>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="media/a.png" Type="image"/></Relationships>`
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"word/document.xml": doc, "word/numbering.xml": numbering, "word/_rels/document.xml.rels": rels, "word/media/a.png": png} {
		f, _ := zw.Create(name)
		f.Write([]byte(body))
	}
	zw.Close()

	pl := readPlan(t, buf.Bytes())
	if len(pl.items) != 1 || len(pl.items[0].options) != 3 {
		t.Fatalf("plan = %+v", pl.items)
	}
	b := buildWordItem(pl.items[0], pl, nil, map[string]bool{"rId1": true})
	if len(b.dto.Errors) > 0 || !b.dto.Options[1].Correct || b.dto.Options[1].Label != "B" || b.dto.Options[1].Body != "Anjing" {
		t.Fatalf("dto = %+v", b.dto)
	}
	if !strings.Contains(b.dto.Stem, `data-media-id="`+tempMediaID(1)+`"`) {
		t.Fatalf("stem lost the picture: %q", b.dto.Stem)
	}
	// a picture that will not be stored disappears from the HTML
	b = buildWordItem(pl.items[0], pl, nil, map[string]bool{})
	if strings.Contains(b.dto.Stem, "<img") || strings.Contains(b.dto.Stem, "<p></p>") {
		t.Fatalf("dropped picture still in stem: %q", b.dto.Stem)
	}
}

func TestKeyLetters(t *testing.T) {
	for in, want := range map[string]string{"B": "B", "a, c": "A,C", "A dan C": "A,C", "ac": "A,C", "A/B;D": "A,B,D", "Benar": "", "": ""} {
		if got := strings.Join(keyLetters(in), ","); got != want {
			t.Errorf("keyLetters(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- service: preview and commit ----

type wordStore struct {
	*fakeStore
	items   []modelBank.ItemVersionEntity
	stimuli map[string]StimulusRow
}

func (f *wordStore) CompetencyIDsByCode(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (f *wordStore) CreateItem(_ context.Context, _ *modelBank.ItemEntity, v *modelBank.ItemVersionEntity) error {
	f.items = append(f.items, *v)
	return nil
}
func (f *wordStore) CreateStimulus(_ context.Context, st *modelBank.StimulusEntity, v *modelBank.StimulusVersionEntity) error {
	f.stimuli[st.ID] = StimulusRow{StimulusEntity: *st, VersionNo: 1, Content: v.Content}
	f.stimuli[v.ID] = f.stimuli[st.ID]
	return nil
}
func (f *wordStore) FindStimulus(_ context.Context, id string) (StimulusRow, error) {
	if r, ok := f.stimuli[id]; ok {
		return r, nil
	}
	return StimulusRow{}, errr.ErrNotFound
}
func (f *wordStore) FindStimulusVersion(_ context.Context, id string) (StimulusRow, error) {
	return f.FindStimulus(context.Background(), id)
}

type fakeMedia struct{ uploads int }

func (m *fakeMedia) Upload(context.Context, modelAuth.Principal, string, []byte, modelAudit.Client) (modelMedia.MediaDTO, error) {
	m.uploads++
	return modelMedia.MediaDTO{ID: "0192a000-0000-7000-8000-00000000f00" + string(rune('0'+m.uploads))}, nil
}

func TestImportWordPreviewAndCommit(t *testing.T) {
	base := &fakeStore{access: modelBank.AccessEditor, versions: map[string]modelBank.ItemVersionEntity{}}
	store := &wordStore{fakeStore: base, stimuli: map[string]StimulusRow{}}
	media := &fakeMedia{}
	svc := NewItemBankService(store, fakeTx{}, media, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	data := wordDoc(t,
		"Bacaan: Teks A", "Isi bacaan.",
		"1. Soal pertama", "A. x", "*B. y",
		"2. Soal tanpa kunci", "A. x", "B. y",
		"Akhir bacaan",
		"3. Esai", "Pembahasan: bebas")

	prev, err := svc.ImportWord(ctx, authorP, bankID, "soal.docx", data, false, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if prev.Valid != 2 || prev.Invalid != 1 || prev.Committed || len(store.items) != 0 || prev.Items[0].Stimulus != "Teks A" {
		t.Fatalf("preview = %+v, stored %d", prev, len(store.items))
	}

	res, err := svc.ImportWord(ctx, authorP, bankID, "soal.docx", data, true, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || res.Created != 2 || res.CreatedStimuli != 1 || len(store.items) != 2 {
		t.Fatalf("commit = %+v, stored %d", res, len(store.items))
	}
	if store.items[0].StimulusVersionID == nil || store.items[1].StimulusVersionID != nil {
		t.Fatal("item 1 uses the reading text, item 3 does not")
	}

	var ve *errr.ValidationError
	for name, tc := range map[string]struct {
		file string
		data []byte
	}{
		"not docx":   {"soal.pdf", data},
		"not zip":    {"soal.docx", []byte("hello")},
		"no items":   {"soal.docx", wordDoc(t, "Hanya teks")},
		"empty file": {"soal.docx", nil},
	} {
		if _, err := svc.ImportWord(ctx, authorP, bankID, tc.file, tc.data, false, modelAudit.Client{}); !errors.As(err, &ve) || ve.Fields["file"] == "" {
			t.Errorf("%s: err = %v, want file error", name, err)
		}
	}
	if _, err := svc.ImportWord(ctx, authorP, bankID, "soal.docx", wordDoc(t, "1. Soal", "A. x", "B. y"), true, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("nothing valid: err = %v", err)
	}
	reader := &fakeStore{access: modelBank.AccessViewer, versions: map[string]modelBank.ItemVersionEntity{}}
	ro := NewItemBankService(&wordStore{fakeStore: reader, stimuli: map[string]StimulusRow{}}, fakeTx{}, media, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := ro.ImportWord(ctx, authorP, bankID, "soal.docx", data, false, modelAudit.Client{}); err == nil {
		t.Fatal("a reader of the bank must not import")
	}
}
