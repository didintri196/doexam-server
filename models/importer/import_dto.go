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

// RowError is one validation problem. Row 0 means the whole file.
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
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
	Page    int
	PerPage int
	Kind    Kind // "" = every kind the caller may see
}

// JobDTO is the public shape of an import job.
type JobDTO struct {
	ID                   string     `json:"id"`
	Kind                 Kind       `json:"kind"`
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
	return JobDTO{
		ID: j.ID, Kind: j.Kind, Status: j.Status, FileName: j.FileName,
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
