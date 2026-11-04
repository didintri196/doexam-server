package tenant

import "strings"

// Limits for the participant card settings.
const (
	MaxCardTitleLen    = 60
	MaxCardIDLabelLen  = 30
	MaxCardNoteLen     = 300
	MaxSignerTitleLen  = 80
	MaxSignerNameLen   = 100
	MaxSignerNumberLen = 60
)

// Card defaults shown while a setting is unset.
const (
	DefaultCardTitle   = "Kartu Peserta Ujian"
	DefaultCardIDLabel = "ID peserta"
)

// CardSettings is tenants.settings.card: what the printed participant card shows.
// Zero values mean the default, so cards of tenants that never set anything stay as they were.
// Hide* flags (not Show*) keep "show everything" as the zero value.
type CardSettings struct {
	Title        string `json:"title,omitempty"`
	IDLabel      string `json:"id_label,omitempty"` // label for users.external_id, e.g. NIS or NIP
	Note         string `json:"note,omitempty"`     // plain text under the card
	SignerTitle  string `json:"signer_title,omitempty"`
	SignerName   string `json:"signer_name,omitempty"`
	SignerNumber string `json:"signer_number,omitempty"` // e.g. "NIP. 19800101..."

	SignatureFileID string `json:"signature_file_id,omitempty"` // media_files.id: signature and/or stamp

	HideExternalID bool `json:"hide_external_id,omitempty"`
	HideExam       bool `json:"hide_exam,omitempty"`
	HideSession    bool `json:"hide_session,omitempty"` // session name and room
	HideSeat       bool `json:"hide_seat,omitempty"`
	HideSchedule   bool `json:"hide_schedule,omitempty"`
	HideQR         bool `json:"hide_qr,omitempty"`
}

// CardInput is the `card` object of PATCH /api/v1/tenant. It replaces every card
// setting except the signature image, which has its own endpoint.
type CardInput struct {
	Title          string `json:"title"`
	IDLabel        string `json:"id_label"`
	Note           string `json:"note"`
	SignerTitle    string `json:"signer_title"`
	SignerName     string `json:"signer_name"`
	SignerNumber   string `json:"signer_number"`
	ShowExternalID bool   `json:"show_external_id"`
	ShowExam       bool   `json:"show_exam"`
	ShowSession    bool   `json:"show_session"`
	ShowSeat       bool   `json:"show_seat"`
	ShowSchedule   bool   `json:"show_schedule"`
	ShowQR         bool   `json:"show_qr"`
}

// Normalize trims the text fields; a value equal to its default is stored as unset.
func (in *CardInput) Normalize() {
	for _, f := range []*string{&in.Title, &in.IDLabel, &in.Note, &in.SignerTitle, &in.SignerName, &in.SignerNumber} {
		*f = strings.TrimSpace(*f)
	}
	if in.Title == DefaultCardTitle {
		in.Title = ""
	}
	if in.IDLabel == DefaultCardIDLabel {
		in.IDLabel = ""
	}
}

// Apply returns c with every setting from in (the signature image is kept).
func (in CardInput) Apply(c CardSettings) CardSettings {
	return CardSettings{
		Title: in.Title, IDLabel: in.IDLabel, Note: in.Note,
		SignerTitle: in.SignerTitle, SignerName: in.SignerName, SignerNumber: in.SignerNumber,
		SignatureFileID: c.SignatureFileID,
		HideExternalID:  !in.ShowExternalID, HideExam: !in.ShowExam, HideSession: !in.ShowSession,
		HideSeat: !in.ShowSeat, HideSchedule: !in.ShowSchedule, HideQR: !in.ShowQR,
	}
}

// CardDTO is the effective card settings (defaults applied). SignatureURL is a signed,
// expiring link filled in by the service; the signature is never public like the logo.
type CardDTO struct {
	Title          string  `json:"title"`
	IDLabel        string  `json:"id_label"`
	Note           string  `json:"note"`
	SignerTitle    string  `json:"signer_title"`
	SignerName     string  `json:"signer_name"`
	SignerNumber   string  `json:"signer_number"`
	SignatureURL   *string `json:"signature_url"`
	ShowExternalID bool    `json:"show_external_id"`
	ShowExam       bool    `json:"show_exam"`
	ShowSession    bool    `json:"show_session"`
	ShowSeat       bool    `json:"show_seat"`
	ShowSchedule   bool    `json:"show_schedule"`
	ShowQR         bool    `json:"show_qr"`
}

// ToCardDTO applies the defaults. signatureURL is the signed link of c.SignatureFileID, or nil.
func ToCardDTO(c CardSettings, signatureURL *string) CardDTO {
	return CardDTO{
		Title: orDefault(c.Title, DefaultCardTitle), IDLabel: orDefault(c.IDLabel, DefaultCardIDLabel), Note: c.Note,
		SignerTitle: c.SignerTitle, SignerName: c.SignerName, SignerNumber: c.SignerNumber, SignatureURL: signatureURL,
		ShowExternalID: !c.HideExternalID, ShowExam: !c.HideExam, ShowSession: !c.HideSession,
		ShowSeat: !c.HideSeat, ShowSchedule: !c.HideSchedule, ShowQR: !c.HideQR,
	}
}
