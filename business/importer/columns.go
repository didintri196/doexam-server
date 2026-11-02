package importer

import (
	"fmt"
	"strings"

	modelImport "doexam/models/importer"
	"doexam/pkg/spreadsheet"
)

// Column names (after spreadsheet.NormalizeHeader).
const (
	colUsername   = "username"
	colFullName   = "full_name"
	colEmail      = "email"
	colExternalID = "external_id"
	colPassword   = "password"
	colRoles      = "roles"
	colGroups     = "groups"
	colMustChange = "must_change_password"

	colCode       = "code"
	colName       = "name"
	colKind       = "kind"
	colPeriod     = "period"
	colParentCode = "parent_code"
)

var userColumns = []spreadsheet.Column{
	{Name: colUsername, Required: true, Note: "Untuk login. 3-100 karakter: huruf, angka, . _ @ -. Contoh: NIS/NISN atau ID karyawan."},
	{Name: colFullName, Required: true, Note: "Nama lengkap."},
	{Name: colEmail, Note: "Opsional. Harus unik."},
	{Name: colExternalID, Note: "Opsional. NIS/NISN/ID karyawan. Harus unik."},
	{Name: colPassword, Note: "Opsional. Kosong = dibuatkan password sementara (unduh sekali setelah impor). Minimal 8 karakter."},
	{Name: colRoles, Note: "Opsional. Kode peran dipisah koma: participant, author, proctor, grader, admin. Kosong = participant."},
	{Name: colGroups, Note: "Opsional. Kode grup dipisah koma, grup harus sudah ada. Contoh: X-IPA-1."},
	{Name: colMustChange, Note: "Opsional. ya/tidak. Kosong = ya (wajib ganti password saat login pertama)."},
}

var groupColumns = []spreadsheet.Column{
	{Name: colCode, Required: true, Note: "Kode unik per periode. Dipakai kolom groups saat impor pengguna."},
	{Name: colName, Required: true, Note: "Nama grup."},
	{Name: colKind, Required: true, Note: "level, class, department, team, batch, atau other."},
	{Name: colPeriod, Note: "Opsional. Contoh: 2026/2027 atau Rekrutmen Q1 2027."},
	{Name: colParentCode, Note: "Opsional. Kode grup induk: grup di file ini atau yang sudah ada."},
}

// Template returns the downloadable template for kind.
func Template(kind modelImport.Kind) spreadsheet.Template {
	common := []string{
		"Isi data di sheet \"Data\" mulai baris 2. Jangan ubah nama kolom di baris 1.",
		"Kolom bertanda * wajib diisi. Semua sel berformat teks agar angka 0 di depan (NIS) tidak hilang.",
		"Bisa juga diunggah sebagai CSV (pemisah koma atau titik koma) dengan nama kolom yang sama.",
	}
	if kind == modelImport.KindGroups {
		return spreadsheet.Template{
			Columns: groupColumns,
			Examples: [][]string{
				{"X", "Kelas X", "level", "2026/2027", ""},
				{"X-IPA-1", "X IPA 1", "class", "2026/2027", "X"},
			},
			Instructions: append(common, "Grup induk boleh ditulis di baris mana pun dalam file yang sama."),
		}
	}
	return spreadsheet.Template{
		Columns: userColumns,
		Examples: [][]string{
			{"0051234567", "Budi Santoso", "", "0051234567", "", "participant", "X-IPA-1", ""},
			{"bu.ani", "Ani Wulandari", "ani@sekolah.sch.id", "", "", "author,grader", "", "ya"},
		},
		Instructions: append(common, fmt.Sprintf("Maksimal %d baris per file, ukuran maksimal 5 MB.", modelImport.MaxRows)),
	}
}

// splitList splits "a, b;c" into ["a","b","c"].
func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseBool reads ya/tidak, yes/no, true/false, 1/0. ok is false for anything else.
func parseBool(s string) (v, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ya", "y", "yes", "true", "1":
		return true, true
	case "tidak", "t", "no", "n", "false", "0":
		return false, true
	}
	return false, false
}
