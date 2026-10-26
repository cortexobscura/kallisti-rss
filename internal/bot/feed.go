package bot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/url"
	"strings"
	"unicode"
)

type Entry struct {
	ID, Title, Link, Summary, Image string
	Images                          []string
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func plain(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		t := z.Next()
		if t == html.ErrorToken {
			break
		}
		tok := z.Token()
		switch t {
		case html.StartTagToken:
			if tok.Data == "script" || tok.Data == "style" {
				skip++
			}
			b.WriteByte(' ')
		case html.EndTagToken:
			if (tok.Data == "script" || tok.Data == "style") && skip > 0 {
				skip--
			}
			b.WriteByte(' ')
		case html.TextToken:
			if skip == 0 {
				b.WriteString(tok.Data)
			}
		}
	}
	s = strings.Join(strings.FieldsFunc(b.String(), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	// Prevent accidental channel-wide pings and formatting from feed-controlled text.
	s = strings.ReplaceAll(s, "@", "＠")
	return s
}
func absolute(base, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	u = b.ResolveReference(u)
	if u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
		return ""
	}
	return u.String()
}
func htmlImage(s, base string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		t := z.Next()
		if t == html.ErrorToken {
			return ""
		}
		if t == html.StartTagToken || t == html.SelfClosingTagToken {
			tok := z.Token()
			if tok.Data == "img" {
				for _, a := range tok.Attr {
					if a.Key == "src" {
						if u := absolute(base, a.Val); u != "" {
							return u
						}
					}
				}
			}
		}
	}
}

// Parse accepts RSS 2, RDF RSS and Atom. XML entities are never resolved externally.
func Parse(b []byte, base string) ([]Entry, error) {
	if len(b) > FeedLimit {
		return nil, errors.New("feed too large")
	}
	if e := xmlBounds(b); e != nil {
		return nil, e
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var out []Entry
	depth := 0
	root := ""
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, errors.New("invalid XML")
		}
		switch v := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 64 {
				return nil, errors.New("XML nesting limit")
			}
			if root == "" {
				root = v.Name.Local
			}
			if v.Name.Local == "item" || v.Name.Local == "entry" {
				var raw struct {
					ID          string `xml:"id"`
					GUID        string `xml:"guid"`
					Title       string `xml:"title"`
					Description string `xml:"description"`
					Summary     string `xml:"summary"`
					Encoded     string `xml:"encoded"`
					Contents    []struct {
						Text   string `xml:",chardata"`
						Inner  string `xml:",innerxml"`
						URL    string `xml:"url,attr"`
						Type   string `xml:"type,attr"`
						Medium string `xml:"medium,attr"`
					} `xml:"content"`
					Links []struct {
						Text string `xml:",chardata"`
						Href string `xml:"href,attr"`
						Rel  string `xml:"rel,attr"`
						Type string `xml:"type,attr"`
					} `xml:"link"`
					Enclosures []struct {
						URL  string `xml:"url,attr"`
						Type string `xml:"type,attr"`
					} `xml:"enclosure"`
					Group struct {
						Thumbs []struct {
							URL string `xml:"url,attr"`
						} `xml:"thumbnail"`
						Description string `xml:"description"`
					} `xml:"group"`
					Thumbs []struct {
						URL string `xml:"url,attr"`
					} `xml:"thumbnail"`
				}
				if e := d.DecodeElement(&raw, &v); e != nil {
					return nil, errors.New("invalid entry")
				}
				depth--
				if len(out) >= 1000 {
					return nil, errors.New("entry limit")
				}
				x := Entry{ID: strings.TrimSpace(raw.ID), Title: cut(plain(raw.Title), 300)}
				if x.ID == "" {
					x.ID = strings.TrimSpace(raw.GUID)
				}
				for _, l := range raw.Links {
					if l.Rel == "" || l.Rel == "alternate" {
						s := l.Href
						if s == "" {
							s = l.Text
						}
						if x.Link == "" {
							x.Link = absolute(base, s)
						}
					}
					if l.Rel == "enclosure" && strings.HasPrefix(l.Type, "image/") {
						x.Image = absolute(base, l.Href)
					}
				}
				content := raw.Encoded
				if content == "" {
					content = raw.Description
				}
				if content == "" {
					content = raw.Summary
				}
				for _, c := range raw.Contents {
					if c.URL != "" && (c.Medium == "image" || strings.HasPrefix(c.Type, "image/")) {
						if x.Image == "" {
							x.Image = absolute(base, c.URL)
						}
					} else if c.Text != "" {
						content = c.Text
					} else if c.Inner != "" {
						content = c.Inner
					}
				}
				for _, e := range raw.Enclosures {
					if x.Image == "" && strings.HasPrefix(e.Type, "image/") {
						x.Image = absolute(base, e.URL)
					}
				}
				if x.Image == "" {
					x.Image = htmlImage(content, base)
				}
				for _, t := range raw.Thumbs {
					if x.Image == "" {
						x.Image = absolute(base, t.URL)
					}
				}
				for _, th := range raw.Group.Thumbs {
					if x.Image == "" {
						x.Image = absolute(base, th.URL)
					}
					x.Images = appendImage(x.Images, absolute(base, th.URL))
				}
				if content == "" {
					content = raw.Group.Description
				}
				x.Images = appendImage(x.Images, x.Image)
				for _, c := range raw.Contents {
					if c.URL != "" && (c.Medium == "image" || strings.HasPrefix(c.Type, "image/")) {
						x.Images = appendImage(x.Images, absolute(base, c.URL))
					}
				}
				for _, en := range raw.Enclosures {
					if strings.HasPrefix(en.Type, "image/") {
						x.Images = appendImage(x.Images, absolute(base, en.URL))
					}
				}
				for _, im := range htmlImages(content, base) {
					x.Images = appendImage(x.Images, im)
				}
				for _, th := range raw.Thumbs {
					x.Images = appendImage(x.Images, absolute(base, th.URL))
				}
				x.Summary = cut(plain(content), 1000)
				if x.ID == "" {
					x.ID = x.Link
				}
				if x.ID == "" {
					sum := sha256.Sum256([]byte(x.Title + "\x00" + x.Summary))
					x.ID = hex.EncodeToString(sum[:])
				}
				x.ID = hexDigest(x.ID)
				if x.Title != "" || x.Link != "" {
					out = append(out, x)
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	if root != "rss" && root != "feed" && root != "RDF" {
		return nil, errors.New("not RSS or Atom")
	}
	return out, nil
}
func hexDigest(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func Text(e Entry) string       { return cut(strings.TrimSpace(e.Title+"\n"+e.Link+"\n"+e.Summary), 2000) }
func Raster(b []byte) ([]byte, error) {
	if len(b) > MediaLimit {
		return nil, errors.New("image byte limit")
	}
	c, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || format != "png" && format != "jpeg" && format != "gif" && format != "webp" {
		return nil, errors.New("unsupported raster")
	}
	if c.Width <= 0 || c.Height <= 0 || c.Width > 8192 || c.Height > 8192 || int64(c.Width)*int64(c.Height) > 16000000 {
		return nil, errors.New("image pixel limit")
	}
	im, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("invalid raster")
	}
	var out bytes.Buffer
	if e = png.Encode(&out, im); e != nil {
		return nil, e
	}
	if out.Len() > MediaLimit {
		return nil, errors.New("normalized image too large")
	}
	return out.Bytes(), nil
}

func xmlBounds(b []byte) error {
	d := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	for {
		t, e := d.Token()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return errors.New("invalid XML")
		}
		switch t.(type) {
		case xml.StartElement:
			depth++
			if depth > 64 {
				return errors.New("XML nesting limit")
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return errors.New("XML directives are not allowed")
		}
	}
}

func appendImage(images []string, u string) []string {
	if u == "" || len(images) >= 10 {
		return images
	}
	for _, x := range images {
		if x == u {
			return images
		}
	}
	return append(images, u)
}
func htmlImages(s, base string) []string {
	var out []string
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		t := z.Next()
		if t == html.ErrorToken {
			return out
		}
		if t == html.StartTagToken || t == html.SelfClosingTagToken {
			tok := z.Token()
			if tok.Data == "img" {
				for _, a := range tok.Attr {
					if a.Key == "src" {
						out = appendImage(out, absolute(base, a.Val))
					}
				}
			}
		}
	}
}
