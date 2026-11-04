package itembank

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	modelMedia "doexam/models/media"
	"doexam/pkg/docx"
	"doexam/pkg/errr"
	"doexam/pkg/richtext"
)

// maxPreviewMediaBytes caps the pictures sent back inline (data: URLs) with a preview.
const maxPreviewMediaBytes = 12 << 20

var reKeySplit = regexp.MustCompile(`(?i)\s*(?:,|;|/|\bdan\b|\band\b|\s)\s*`)

// builtItem is a planned item turned into create input, plus what the preview shows.
type builtItem struct {
	src   *wordItem
	input modelBank.ItemInput
	dto   modelBank.WordItemDTO
}

// ImportWord reads a .docx in the import format (see WordTemplate). With commit false it
// only returns a preview: every item as read, with its problems, and nothing is stored.
// With commit true it stores the pictures and creates every valid item (as a draft) and
// the reading texts they use; invalid items are skipped and reported.
func (s *ItemBankService) ImportWord(ctx context.Context, p modelAuth.Principal, bankID, filename string, data []byte, commit bool, client modelAudit.Client) (modelBank.WordImportDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ItemBankService.ImportWord", trace.WithAttributes(attribute.String("bank.id", bankID), attribute.Bool("commit", commit)))
	defer span.End()

	if len(data) == 0 || len(data) > modelBank.MaxWordBytes {
		return modelBank.WordImportDTO{}, fail(span, fileErr("required, max 20 MB"))
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".docx") || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return modelBank.WordImportDTO{}, fail(span, fileErr("use a .docx file (Word 2007 or newer)"))
	}
	doc, err := docx.Read(data)
	switch {
	case errors.Is(err, docx.ErrTooLarge):
		return modelBank.WordImportDTO{}, fail(span, fileErr("document or picture too large"))
	case err != nil:
		return modelBank.WordImportDTO{}, fail(span, fileErr("cannot read the document"))
	}
	plan := planWord(doc)
	if len(plan.items) == 0 {
		return modelBank.WordImportDTO{}, fail(span, fileErr("no items found; number them 1., 2., … (see the template)"))
	}
	if len(plan.items) > modelBank.MaxWordItems {
		return modelBank.WordImportDTO{}, fail(span, fileErr(fmt.Sprintf("max %d items per file", modelBank.MaxWordItems)))
	}

	var (
		bank  modelBank.BankEntity
		comps map[string]string
	)
	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		var err error
		if bank, _, err = s.bankAccess(ctx, p, bankID, levelWrite); err != nil {
			return err
		}
		comps, err = s.storer.CompetencyIDsByCode(ctx, bank.SubjectID)
		return err
	})
	if err != nil {
		return modelBank.WordImportDTO{}, fail(span, err)
	}

	out := modelBank.WordImportDTO{Items: []modelBank.WordItemDTO{}, Warnings: append([]string{}, doc.Warnings...), Stimuli: len(plan.stimuli)}
	out.Warnings = append(out.Warnings, plan.warnings...)
	pictures := checkPictures(doc, plan, &out)
	built := make([]builtItem, 0, len(plan.items))
	for _, it := range plan.items {
		b := buildWordItem(it, plan, comps, pictures)
		built = append(built, b)
		out.Items = append(out.Items, b.dto)
		if len(b.dto.Errors) == 0 {
			out.Valid++
		} else {
			out.Invalid++
		}
	}

	if !commit {
		out.Media = previewMedia(doc, plan, pictures)
		return out, nil
	}
	if out.Valid == 0 {
		return out, fail(span, errr.WithMessage(errr.ErrInvalidInput, "no valid items to import"))
	}
	if s.media == nil {
		return out, fail(span, errors.New("media store not configured"))
	}

	// Store the pictures the valid items use (MediaStore opens its own transaction).
	used := map[string]bool{}
	stimUsed := map[int]bool{}
	for _, b := range built {
		if len(b.dto.Errors) > 0 {
			continue
		}
		if b.src.stimulus >= 0 {
			stimUsed[b.src.stimulus] = true
		}
		collectMedia(used, b.input)
	}
	for i := range stimUsed {
		for _, id := range richtext.MediaIDs(strings.Join(plan.stimuli[i].body, "")) {
			used[id] = true
		}
	}
	realID := map[string]string{}
	for rid, temp := range plan.images {
		if !used[temp] || !pictures[rid] {
			continue
		}
		img := doc.Media[rid]
		m, err := s.media.Upload(ctx, p, img.Name, img.Data, client)
		if err != nil {
			return out, fail(span, fmt.Errorf("store picture %s: %w", img.Name, err))
		}
		realID[temp] = m.ID
	}
	swap := func(html string) string {
		for temp, id := range realID {
			html = strings.ReplaceAll(html, temp, id)
		}
		return html
	}

	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		bank, _, err := s.bankAccess(ctx, p, bankID, levelWrite)
		if err != nil {
			return err
		}
		stimIDs := map[int]string{}
		for i := range plan.stimuli {
			if !stimUsed[i] {
				continue
			}
			st := plan.stimuli[i]
			v := errr.Validation{}
			body := sanitizeField(swap(strings.Join(st.body, "")), "body", true, v)
			if err := v.Err(); err != nil {
				return fmt.Errorf("reading text %q: %w", st.title, err)
			}
			dto, err := s.insertStimulus(ctx, p, bank.ID, st.title, body, client)
			if err != nil {
				return err
			}
			stimIDs[i] = dto.ID
			out.CreatedStimuli++
		}
		for _, b := range built {
			if len(b.dto.Errors) > 0 {
				continue
			}
			in := swapInput(b.input, swap)
			if b.src.stimulus >= 0 {
				id := stimIDs[b.src.stimulus]
				in.StimulusID = &id
			}
			it, ver, err := s.prepareItem(p, bank.ID, &in)
			if err != nil {
				return fmt.Errorf("item %d: %w", b.src.no, err)
			}
			if _, err := s.insertItem(ctx, p, bank, in, it, ver, client); err != nil {
				return fmt.Errorf("item %d: %w", b.src.no, err)
			}
			out.Created++
		}
		return nil
	})
	if err != nil {
		return modelBank.WordImportDTO{}, fail(span, err)
	}
	out.Committed = true
	s.logger.InfoContext(ctx, "word import", "bank_id", bankID, "created", out.Created, "skipped", out.Invalid)
	return out, nil
}

// checkPictures keeps the pictures the media store accepts (PNG/JPEG/GIF/WebP ≤ 5 MB)
// and warns about the others (EMF/WMF drawings, huge scans).
func checkPictures(d *docx.Document, pl *wordPlan, out *modelBank.WordImportDTO) map[string]bool {
	ok := map[string]bool{}
	for rid := range pl.images {
		img := d.Media[rid]
		_, known := modelMedia.ImageTypes[http.DetectContentType(img.Data)]
		switch {
		case !known:
			out.Warnings = append(out.Warnings, fmt.Sprintf("picture %s skipped: use PNG, JPEG, GIF or WebP", img.Name))
		case len(img.Data) > modelMedia.MaxImageBytes:
			out.Warnings = append(out.Warnings, fmt.Sprintf("picture %s skipped: larger than 5 MB", img.Name))
		default:
			ok[rid] = true
		}
	}
	slices.Sort(out.Warnings)
	out.Warnings = slices.Compact(out.Warnings)
	return ok
}

// previewMedia returns data: URLs for the pictures so the preview can show them.
func previewMedia(d *docx.Document, pl *wordPlan, pictures map[string]bool) map[string]modelMedia.Ref {
	out := map[string]modelMedia.Ref{}
	total := 0
	for rid, temp := range pl.images {
		img := d.Media[rid]
		if !pictures[rid] || total+len(img.Data) > maxPreviewMediaBytes {
			continue
		}
		total += len(img.Data)
		mime := http.DetectContentType(img.Data)
		out[temp] = modelMedia.Ref{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data), Mime: mime}
	}
	return out
}

// dropPictures removes pictures that will not be stored from HTML.
func dropPictures(html string, pl *wordPlan, pictures map[string]bool) string {
	for rid, temp := range pl.images {
		if !pictures[rid] {
			html = strings.ReplaceAll(html, `<img data-media-id="`+temp+`" alt="">`, "")
		}
	}
	return html
}

// buildWordItem decides the type and the key, then validates with the same rules as the editor.
func buildWordItem(it *wordItem, pl *wordPlan, comps map[string]string, pictures map[string]bool) builtItem {
	b := builtItem{src: it}
	d := &b.dto
	d.No, d.Key, d.Errors, d.Warnings = it.no, it.key, []modelBank.WordIssue{}, []modelBank.WordIssue{}
	issue := func(field, msg string) { d.Errors = append(d.Errors, modelBank.WordIssue{Field: field, Message: msg}) }
	warn := func(msg string) { d.Warnings = append(d.Warnings, modelBank.WordIssue{Message: msg}) }

	stem := dropPictures(strings.Join(it.stem, ""), pl, pictures)
	options := make([]wordOption, len(it.options))
	for i, o := range it.options {
		o.body = dropPictures(o.body, pl, pictures)
		if o.label == "" {
			o.label = string(rune('A' + i))
		}
		options[i] = o
	}
	if it.stimulus >= 0 {
		d.Stimulus = pl.stimuli[it.stimulus].title
		if len(pl.stimuli[it.stimulus].body) == 0 {
			issue("stimulus", "reading text is empty")
		}
	}

	// type: as declared with "Jenis:", else from the content
	typ := modelBank.ItemType(it.typ)
	if typ == "" {
		switch {
		case len(options) > 0 && (len(keyLetters(it.key)) > 1 || countMarked(options) > 1):
			typ = modelBank.TypeMCQMulti
		case len(options) > 0:
			typ = modelBank.TypeMCQSingle
		case it.key != "":
			typ = modelBank.TypeShortAnswer
		default:
			typ = modelBank.TypeEssay
		}
	}
	d.Type = typ

	content := modelBank.Content{Stem: stem}
	var key any
	switch typ {
	case modelBank.TypeMCQSingle, modelBank.TypeMCQMulti:
		correct := correctOptions(it.key, options)
		if it.key != "" && len(correct) == 0 {
			issue("answer_key", "answer key letter does not match an option")
		}
		ids := []string{}
		for i, o := range options {
			id := strings.ToLower(o.label)
			if slices.Contains(ids, id) {
				id = fmt.Sprintf("o%d", i+1)
			}
			ids = append(ids, id)
			content.Options = append(content.Options, modelBank.Option{ID: id, Body: o.body})
			d.Options = append(d.Options, modelBank.WordOptionDTO{Label: o.label, Body: o.body, Correct: slices.Contains(correct, i)})
		}
		picked := []string{}
		for _, i := range correct {
			picked = append(picked, ids[i])
		}
		if typ == modelBank.TypeMCQSingle {
			if len(picked) > 1 {
				issue("answer_key", "single choice needs exactly one answer; use Jenis: pgk for several")
			}
			if len(picked) > 0 {
				key = map[string]any{"correct": picked[0]}
			}
		} else if len(picked) > 0 {
			key = map[string]any{"correct": picked}
		}
		if it.key == "" && len(correct) == 0 {
			issue("answer_key", "answer key missing: add \"Kunci: B\" or mark the option with *")
		}
	case modelBank.TypeTrueFalse:
		switch strings.ToLower(strings.TrimSpace(it.key)) {
		case "b", "benar", "true", "t", "ya", "betul":
			key = map[string]any{"value": true}
		case "s", "salah", "false", "f", "tidak":
			key = map[string]any{"value": false}
		case "":
			issue("answer_key", "answer key missing: \"Kunci: Benar\" or \"Kunci: Salah\"")
		default:
			issue("answer_key", "true/false key must be Benar or Salah")
		}
		if len(options) > 0 {
			warn("options of a true/false item are ignored")
		}
	case modelBank.TypeShortAnswer:
		var accepted []modelBank.AcceptedAnswer
		numeric := true
		for _, a := range strings.Split(it.key, "|") {
			if a = strings.TrimSpace(a); a != "" {
				accepted = append(accepted, modelBank.AcceptedAnswer{Value: a})
				if _, err := strconv.ParseFloat(strings.ReplaceAll(a, ",", "."), 64); err != nil {
					numeric = false
				}
			}
		}
		if len(accepted) == 0 {
			issue("answer_key", "answer key missing: \"Kunci: jawaban\" (several: jawaban1 | jawaban2)")
		}
		content.Input = "text"
		if numeric && len(accepted) > 0 {
			content.Input = "number"
		}
		key = map[string]any{"accepted": accepted}
		if len(options) > 0 {
			warn("options of a short-answer item are ignored")
		}
	case modelBank.TypeEssay:
		if it.key != "" {
			key = map[string]any{"model_answer": "<p>" + escapeText(it.key) + "</p>"}
		}
		if len(options) > 0 {
			warn("options of an essay item are ignored")
		}
	}

	b.input = modelBank.ItemInput{VersionInput: modelBank.VersionInput{Type: typ, Content: mustJSON(content)}}
	if key != nil {
		b.input.AnswerKey = mustJSON(key)
	}
	if ex := dropPictures(strings.Join(it.explanation, ""), pl, pictures); ex != "" {
		b.input.Explanation = &ex
		d.Explanation = ex
	}
	if it.points != "" {
		v, err := strconv.ParseFloat(strings.ReplaceAll(it.points, ",", "."), 64)
		if err != nil || v <= 0 || v > 1000 {
			issue("default_points", "score must be a number from 0 to 1000")
		} else {
			b.input.DefaultPoints = &v
			d.Points = &v
		}
	}
	if it.competency != "" {
		d.Competency = it.competency
		if id, ok := comps[strings.ToLower(it.competency)]; ok {
			b.input.CompetencyID = &id
		} else {
			issue("competency", fmt.Sprintf("competency %q not found in this bank's subject", it.competency))
		}
	}

	// the editor's validation: types, options, keys, sanitizing
	v := errr.Validation{}
	copyIn := b.input
	normalizeMeta(&copyIn.VersionInput, v)
	canon := normalizeBody(typ, copyIn.Content, copyIn.AnswerKey, copyIn.Scoring, v)
	if err := v.Err(); err != nil {
		var ve *errr.ValidationError
		if errors.As(err, &ve) {
			fields := make([]string, 0, len(ve.Fields))
			for f := range ve.Fields {
				fields = append(fields, f)
			}
			slices.Sort(fields)
			for _, f := range fields {
				if f == "answer_key.correct" && hasIssue(d.Errors, "answer_key") {
					continue // already explained in the author's terms
				}
				issue(f, ve.Fields[f])
			}
		}
	}
	var c modelBank.Content
	if json.Unmarshal(canon.Content, &c) == nil && c.Stem != "" {
		d.Stem = c.Stem
		for i := range d.Options {
			if i < len(c.Options) {
				d.Options[i].Body = c.Options[i].Body
			}
		}
	} else if clean, err := richtext.Sanitize(stem); err == nil {
		d.Stem = clean
	}
	return b
}

func hasIssue(list []modelBank.WordIssue, field string) bool {
	return slices.ContainsFunc(list, func(i modelBank.WordIssue) bool { return i.Field == field })
}

// keyLetters reads "B", "A, C", "A dan C", "ac" into upper-case letters.
func keyLetters(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	var out []string
	for _, part := range reKeySplit.Split(key, -1) {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if !regexp.MustCompile(`^[A-J]+$`).MatchString(part) {
			return nil
		}
		for _, r := range part {
			if l := string(r); !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// correctOptions returns the indexes of the correct options: from "Kunci:", else the
// options marked with *, else (only when not every option is bold) the bold ones.
func correctOptions(key string, options []wordOption) []int {
	var out []int
	if key != "" {
		for _, l := range keyLetters(key) {
			for i, o := range options {
				if o.label == l {
					out = append(out, i)
				}
			}
		}
		return out
	}
	for i, o := range options {
		if o.marked {
			out = append(out, i)
		}
	}
	if len(out) > 0 {
		return out
	}
	bold := 0
	for _, o := range options {
		if o.bold {
			bold++
		}
	}
	if bold == len(options) {
		return nil
	}
	for i, o := range options {
		if o.bold {
			out = append(out, i)
		}
	}
	return out
}

func countMarked(options []wordOption) int {
	n := 0
	for _, o := range options {
		if o.marked {
			n++
		}
	}
	return n
}

func collectMedia(into map[string]bool, in modelBank.ItemInput) {
	for _, doc := range []string{string(in.Content), string(in.AnswerKey)} {
		for _, id := range richtext.MediaIDs(strings.ReplaceAll(doc, `\"`, `"`)) {
			into[id] = true
		}
	}
	if in.Explanation != nil {
		for _, id := range richtext.MediaIDs(*in.Explanation) {
			into[id] = true
		}
	}
}

func swapInput(in modelBank.ItemInput, swap func(string) string) modelBank.ItemInput {
	in.Content = json.RawMessage(swap(string(in.Content)))
	if in.AnswerKey != nil {
		in.AnswerKey = json.RawMessage(swap(string(in.AnswerKey)))
	}
	if in.Explanation != nil {
		ex := swap(*in.Explanation)
		in.Explanation = &ex
	}
	return in
}

func escapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func fileErr(msg string) error { return &errr.ValidationError{Fields: map[string]string{"file": msg}} }
