package itembank

import (
	"bytes"

	"doexam/pkg/docx"
)

// wordTemplate explains the import format with an example of every item type.
var wordTemplate = []docx.Line{
	"**Templat impor soal DoExam**",
	"Hapus bagian petunjuk ini lalu tulis soal Anda dengan pola yang sama. Gambar, tabel, tebal/miring, subskrip/superskrip, dan rumus (Insert → Equation) ikut terbaca.",
	"Aturan: soal diberi nomor 1., 2., … (atau penomoran otomatis Word); pilihan diberi huruf A., B., … (atau huruf otomatis). Tandai jawaban dengan baris Kunci: atau tanda * di depan huruf pilihan.",
	"Baris opsional di bawah soal: Skor: 2, Kompetensi: <kode kompetensi>, Pembahasan: <teks>. Jenis: berlaku untuk soal-soal sesudahnya (pg, pgk, bs, isian, esai). Tanpa Jenis:, soal berpilihan dianggap pg, soal dengan Kunci tanpa pilihan dianggap isian, sisanya esai.",
	"",
	"**Jenis: pg**",
	"1. Ibu kota provinsi Jawa Barat adalah …",
	"A. Semarang",
	"*B. Bandung",
	"C. Surabaya",
	"D. Serang",
	"Skor: 1",
	"Pembahasan: Bandung adalah ibu kota Jawa Barat.",
	"",
	"**Jenis: pgk**",
	"2. Manakah yang termasuk bilangan prima?",
	"A. 2",
	"B. 4",
	"C. 7",
	"D. 9",
	"Kunci: A, C",
	"",
	"**Jenis: bs**",
	"3. Air mendidih pada suhu 100 °C di permukaan laut.",
	"Kunci: Benar",
	"",
	"**Jenis: isian**",
	"4. Hasil dari 12 × 12 adalah …",
	"Kunci: 144",
	"",
	"**Jenis: esai**",
	"5. Jelaskan dampak pemanasan global bagi wilayah pesisir.",
	"Kunci: Contoh jawaban: kenaikan muka laut, abrasi, intrusi air laut.",
	"",
	"**Jenis: pg**",
	"Bacaan: Teks Hutan Mangrove",
	"Hutan mangrove melindungi pantai dari abrasi dan menjadi tempat berkembang biak berbagai ikan. Bacaan dipakai oleh soal-soal sesudahnya sampai baris Akhir bacaan.",
	"6. Fungsi utama hutan mangrove menurut bacaan adalah …",
	"A. Menghasilkan kayu",
	"*B. Melindungi pantai dari abrasi",
	"C. Tempat wisata",
	"Akhir bacaan",
}

// WordTemplate returns the downloadable .docx template of the import format.
func WordTemplate() ([]byte, error) {
	var buf bytes.Buffer
	if err := docx.Write(&buf, wordTemplate); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
