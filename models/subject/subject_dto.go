package subject

import (
	"strings"
	"time"
)

// ListFilter pages GET /api/v1/subjects.
type ListFilter struct {
	Page    int
	PerPage int
	Query   string // matches code and name
}

// SubjectInput is the body of POST /api/v1/subjects.
type SubjectInput struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// SubjectUpdateInput is the body of PATCH /api/v1/subjects/:id.
type SubjectUpdateInput struct {
	Code *string `json:"code"`
	Name *string `json:"name"`
}

// CompetencyInput is the body of POST /api/v1/subjects/:id/competencies.
type CompetencyInput struct {
	ParentID  *string `json:"parent_id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
}

// CompetencyUpdateInput is the body of PATCH /api/v1/competencies/:id.
// parent_id "" moves the competency to the root.
type CompetencyUpdateInput struct {
	ParentID  *string `json:"parent_id"`
	Code      *string `json:"code"`
	Name      *string `json:"name"`
	SortOrder *int    `json:"sort_order"`
}

// Normalize trims text fields.
func (in *SubjectInput) Normalize() {
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
}

// Normalize trims text fields.
func (in *SubjectUpdateInput) Normalize() { trimPtrs(&in.Code, &in.Name) }

// Normalize trims text fields; an empty parent_id becomes nil.
func (in *CompetencyInput) Normalize() {
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	trimPtrs(&in.ParentID)
	if in.ParentID != nil && *in.ParentID == "" {
		in.ParentID = nil
	}
}

// Normalize trims text fields; "" parent_id is kept (= move to root).
func (in *CompetencyUpdateInput) Normalize() { trimPtrs(&in.ParentID, &in.Code, &in.Name) }

// SubjectDTO is the public shape of a subject.
type SubjectDTO struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CompetencyDTO is one node of a competency tree; clients build the tree from parent_id.
type CompetencyDTO struct {
	ID        string  `json:"id"`
	SubjectID string  `json:"subject_id"`
	ParentID  *string `json:"parent_id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
}

// ToDTO converts a SubjectEntity.
func ToDTO(e SubjectEntity) SubjectDTO {
	return SubjectDTO{ID: e.ID, Code: e.Code, Name: e.Name, CreatedAt: e.CreatedAt}
}

// ToCompetencyDTO converts a CompetencyEntity.
func ToCompetencyDTO(e CompetencyEntity) CompetencyDTO {
	return CompetencyDTO{ID: e.ID, SubjectID: e.SubjectID, ParentID: e.ParentID, Code: e.Code, Name: e.Name, SortOrder: e.SortOrder}
}

func trimPtrs(ps ...**string) {
	for _, p := range ps {
		if *p != nil {
			v := strings.TrimSpace(**p)
			*p = &v
		}
	}
}
