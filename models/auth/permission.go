package auth

import "slices"

// Permission codes stored in roles.permissions.
const (
	PermTenantManage   = "tenant.manage"
	PermUserManage     = "user.manage"
	PermGroupManage    = "group.manage"
	PermItemRead       = "item.read"
	PermItemWrite      = "item.write"
	PermExamRead       = "exam.read"
	PermExamWrite      = "exam.write"
	PermExamPublish    = "exam.publish"
	PermSessionManage  = "session.manage"
	PermSessionProctor = "session.proctor"
	PermGradeWrite     = "grade.write"
	PermReportRead     = "report.read"
	PermAttemptTake    = "attempt.take"
)

// System role codes (roles.code). Created for every tenant, flagged is_system.
const (
	RoleAdmin       = "admin"
	RoleAuthor      = "author"
	RoleProctor     = "proctor"
	RoleGrader      = "grader"
	RoleParticipant = "participant"
)

// SystemRole is the template for a built-in role.
type SystemRole struct {
	Code        string
	Name        string
	Permissions []string
}

// SystemRoles returns the built-in roles of a new tenant. Display names are neutral
// (no school or company wording) for every preset; admins may rename roles afterwards.
func SystemRoles() []SystemRole {
	names := map[string]string{
		RoleAdmin: "Administrator", RoleAuthor: "Penyusun Soal", RoleProctor: "Pengawas",
		RoleGrader: "Penilai", RoleParticipant: "Peserta",
	}

	return []SystemRole{
		{Code: RoleAdmin, Name: names[RoleAdmin], Permissions: []string{
			PermTenantManage, PermUserManage, PermGroupManage, PermItemRead, PermItemWrite,
			PermExamRead, PermExamWrite, PermExamPublish, PermSessionManage, PermSessionProctor,
			PermGradeWrite, PermReportRead,
		}},
		{Code: RoleAuthor, Name: names[RoleAuthor], Permissions: []string{
			PermItemRead, PermItemWrite, PermExamRead, PermExamWrite, PermReportRead,
		}},
		{Code: RoleProctor, Name: names[RoleProctor], Permissions: []string{
			PermExamRead, PermSessionProctor,
		}},
		{Code: RoleGrader, Name: names[RoleGrader], Permissions: []string{
			PermExamRead, PermGradeWrite, PermReportRead,
		}},
		{Code: RoleParticipant, Name: names[RoleParticipant], Permissions: []string{
			PermAttemptTake,
		}},
	}
}

// MergePermissions returns the sorted, de-duplicated union of the roles' permissions.
func MergePermissions(roles []RoleEntity) []string {
	var out []string
	for _, r := range roles {
		out = append(out, r.Permissions...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
