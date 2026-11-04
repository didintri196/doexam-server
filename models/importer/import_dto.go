package importer

import (
	"encoding/json"
	"time"
)

// Limits of one upload.
const (
	MaxFileBytes    = 5 << 20 // 5 MiB
	MaxRows         = 2000
	MaxStoredErrors = 500 // error_rows still counts every failing row
)

// RowError is one validation problem. Row 0 means the whole file. A warning does not
// stop the import (e.g. an ignored column, a name that differs from the account).
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
}

// Credential is a generated password for an imported user, downloadable once.
type Credential struct {
	Row       int
	Username  string
	FullName  string
	Password  string
	Generated bool
}

// ListFilter pages GET /api/v1/imports.
type ListFilter struct {
	Page     int
	PerPage  int
	Kind     Kind     // "" = every kind the caller may see
	Audience Audience // "" = any
	GroupID  string   // "" = any
}

// JobDTO is the public shape of an import job.
type JobDTO struct {
	ID                   string     `json:"id"`
	Kind                 Kind       `json:"kind"`
	Audience             Audience   `json:"audience"`
	GroupID              *string    `json:"group_id"`
	Summary              Summary    `json:"summary"`
	Status               Status     `json:"status"`
	FileName             string     `json:"file_name"`
	TotalRows            int        `json:"total_rows"`
	OkRows               int        `json:"ok_rows"`
	ErrorRows            int        `json:"error_rows"`
	Errors               []RowError `json:"errors"`
	CredentialsAvailable bool       `json:"credentials_available"`
	CreatedAt            time.Time  `json:"created_at"`
	FinishedAt           *time.Time `json:"finished_at"`
}

// ToDTO converts a JobRow to JobDTO.
func ToDTO(j JobRow, credentialsAvailable bool) JobDTO {
	errs := []RowError{}
	if len(j.Errors) > 0 {
		_ = json.Unmarshal(j.Errors, &errs) // written by us; a bad value just shows no errors
	}
	t := DecodeTarget(j.Target)
	var sum Summary
	_ = json.Unmarshal(j.Summary, &sum)
	var group *string
	if t.GroupID != "" {
		group = &t.GroupID
	}
	return JobDTO{
		ID: j.ID, Kind: j.Kind, Audience: t.Audience, GroupID: group, Summary: sum, Status: j.Status, FileName: j.FileName,
		TotalRows: j.TotalRows, OkRows: j.OkRows, ErrorRows: j.ErrorRows, Errors: errs,
		CredentialsAvailable: credentialsAvailable, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt,
	}
}

// EncodeErrors serialises errs for import_jobs.errors, keeping at most MaxStoredErrors.
func EncodeErrors(errs []RowError) []byte {
	if len(errs) > MaxStoredErrors {
		errs = errs[:MaxStoredErrors]
	}
	if errs == nil {
		errs = []RowError{}
	}
	b, _ := json.Marshal(errs) // plain structs: cannot fail
	return b
}

// DecodeTarget parses import_jobs.target; unknown content yields the empty target.
func DecodeTarget(raw []byte) Target {
	var t Target
	_ = json.Unmarshal(raw, &t)
	return t
}

// Encode returns the JSON stored in import_jobs.target.
func (t Target) Encode() []byte {
	b, _ := json.Marshal(t) // plain strings: cannot fail
	return b
}

// Encode returns the JSON stored in import_jobs.summary.
func (s Summary) Encode() []byte {
	b, _ := json.Marshal(s)
	return b
}
