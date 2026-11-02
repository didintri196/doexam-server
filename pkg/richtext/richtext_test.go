package richtext

import (
	"errors"
	"strings"
	"testing"
)

const mediaID = "0192a000-0000-7000-8000-000000000123"

func TestSanitize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain formatting kept", `<p>Hitung <strong>2</strong><sup>3</sup></p>`, `<p>Hitung <strong>2</strong><sup>3</sup></p>`},
		{"math kept", `<p><span data-math="\frac{1}{2}"></span></p>`, `<p><span data-math="\frac{1}{2}"></span></p>`},
		{"math block kept", `<div data-math-block="x^2"></div>`, `<div data-math-block="x^2"></div>`},
		{"script removed", `<p>a</p><script>alert(1)</script>`, `<p>a</p>`},
		{"event handler removed", `<p onclick="alert(1)">a</p>`, `<p>a</p>`},
		{"style removed", `<p style="color:red">a</p><style>p{}</style>`, `<p>a</p>`},
		{"iframe removed", `<iframe src="https://evil"></iframe>b`, `b`},
		{"link becomes text", `<a href="javascript:alert(1)">x</a>`, `x`},
		{"img src dropped, media id kept", `<img src="https://evil/x.png" data-media-id="` + mediaID + `" alt="grafik" onerror="x()">`,
			`<img data-media-id="` + mediaID + `" alt="grafik">`},
		{"img without media id dropped", `<img src="https://evil/x.png">`, ``},
		{"bad media id dropped", `<img data-media-id="../../etc/passwd">`, ``},
		{"allowed class kept", `<p class="align-center">a</p>`, `<p class="align-center">a</p>`},
		{"unknown class dropped", `<p class="evil">a</p>`, `<p>a</p>`},
		{"rtl kept", `<p dir="rtl" lang="ar">بسم</p>`, `<p dir="rtl" lang="ar">بسم</p>`},
		{"attribute escaped", `<span data-math="&quot;><script>x</script>"></span>`, `<span data-math="&#34;&gt;&lt;script&gt;x&lt;/script&gt;"></span>`},
		{"table spans", `<table><tbody><tr><td colspan="2" rowspan="999">a</td></tr></tbody></table>`, `<table><tbody><tr><td colspan="2">a</td></tr></tbody></table>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Sanitize(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Sanitize(%q)\n got  %q\n want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeTooLarge(t *testing.T) {
	if _, err := Sanitize("<p>" + strings.Repeat("a", MaxBytes) + "</p>"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestPlainTextAndBlank(t *testing.T) {
	h := `<p>Hitung<span data-math="\frac{1}{2}"></span>dari</p><ul><li>10</li><li>20</li></ul>`
	if got := PlainText(h); got != `Hitung \frac{1}{2} dari 10 20` {
		t.Fatalf("PlainText = %q", got)
	}
	if !IsBlank("<p> </p><br>") {
		t.Fatal("empty paragraphs are blank")
	}
	if IsBlank(`<img data-media-id="` + mediaID + `">`) {
		t.Fatal("an image is not blank")
	}
	if IsBlank(`<span data-math="x"></span>`) {
		t.Fatal("a formula is not blank")
	}
}
