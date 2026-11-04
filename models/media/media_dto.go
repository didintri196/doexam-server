package media

// MaxImageBytes bounds an uploaded image (POST /api/v1/media).
const MaxImageBytes = 5 << 20

// MaxAudioBytes bounds an uploaded audio clip (listening sections run a few minutes).
const MaxAudioBytes = 20 << 20

// MaxUploadBytes is the largest upload of any kind (the handler reads one byte more).
const MaxUploadBytes = MaxAudioBytes

// AudioTypes maps the accepted audio types to the stored extension: MP3, M4A/AAC, Ogg
// (Vorbis/Opus) and WAV play in every current browser.
var AudioTypes = map[string]string{
	"audio/mpeg": "mp3",
	"audio/mp4":  "m4a",
	"audio/ogg":  "ogg",
	"audio/wav":  "wav",
}

// MaxLinks bounds POST /api/v1/media/links.
const MaxLinks = 500

// ImageTypes maps the accepted image MIME types (sniffed from the content) to the file
// extension used in storage. SVG is not accepted: it can carry script.
var ImageTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// URLPrefix is where signed media links point (GET /api/v1/media/:id?t&e&s).
const URLPrefix = "/api/v1/media/"

// MediaDTO is an uploaded file with a signed link to it.
type MediaDTO struct {
	ID        string `json:"id"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Width     *int   `json:"width"`
	Height    *int   `json:"height"`
	URL       string `json:"url"`
}

// Ref is what a renderer needs for one data-media-id: a signed URL plus the type and,
// for images, the intrinsic size.
type Ref struct {
	URL  string `json:"url"`
	Mime string `json:"mime"`
	W    *int   `json:"w,omitempty"`
	H    *int   `json:"h,omitempty"`
}

// LinksInput is the body of POST /api/v1/media/links.
type LinksInput struct {
	IDs []string `json:"ids"`
}

// File is a stored file ready to be sent.
type File struct {
	Data     []byte
	MimeType string
	SHA256   []byte
}
