package tenant

import "testing"

func TestSlugFromName(t *testing.T) {
	tests := map[string]string{
		"SMA Negeri 1 Malang":      "sma-negeri-1-malang",
		"  PT. Maju -- Jaya, Tbk ": "pt-maju-jaya-tbk",
		"SMK Ma'arif NU":           "smk-ma-arif-nu",
		"学校":                       "main",
		"A":                        "main",
	}
	for name, want := range tests {
		if got := SlugFromName(name); got != want {
			t.Errorf("SlugFromName(%q) = %q, want %q", name, got, want)
		}
	}
	long := SlugFromName("Sekolah Menengah Atas Negeri Satu Kota Yang Sangat Panjang Sekali Namanya")
	if len(long) > 63 || !SlugPattern.MatchString(long) {
		t.Errorf("long name gives invalid slug %q", long)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := Settings{Timezone: "Asia/Jayapura", Letterhead: "Jl. A & B <1>"}
	raw := s.Encode()
	if string(raw) != `{"timezone":"Asia/Jayapura","letterhead":"Jl. A & B <1>"}` {
		t.Fatalf("encoded = %s", raw)
	}
	if DecodeSettings(raw) != s {
		t.Fatal("round trip changed settings")
	}
	if DecodeSettings([]byte("not json")) != (Settings{}) || DecodeSettings(nil) != (Settings{}) {
		t.Fatal("malformed settings must decode to zero")
	}
	if string(Settings{}.Encode()) != "{}" {
		t.Fatal("empty settings must encode as {}")
	}
}

func TestToDTODefaults(t *testing.T) {
	d := ToDTO(TenantEntity{Slug: "x", Settings: []byte("{}")})
	if d.Settings.Timezone != DefaultTimezone || d.Settings.Locale != DefaultLocale || d.Settings.PrimaryColor != nil || d.Settings.LogoURL != nil {
		t.Fatalf("defaults = %+v", d.Settings)
	}
}
