package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/gorilla/css/scanner"
	"golang.org/x/net/html"
)

const MaxFrontendDocumentBytes = 24 << 20
const frontendDocumentCSP = "sandbox allow-scripts; default-src 'none'; script-src data:; style-src data: 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

var ErrUnsupportedFrontendDocument = errors.New("application frontend is incompatible with the self-contained document profile")

type FrontendDocument struct {
	SourceSHA256   string
	DocumentSHA256 string
	CSP            string
	Content        []byte
}
type frontendDocumentBuilder struct {
	archive *frontendArchive
	used    int
	encoded map[string]string
}

// BuildFrontendDocument derives one self-contained document from verified bytes.
// It performs no authorization or IO. The HTTP owner must authorize its complete
// build and return the supplied CSP on every document response, including direct
// navigation. This development profile expects bundled classic scripts and CSS.
func BuildFrontendDocument(m Manifest, raw []byte) (FrontendDocument, error) {
	var zero FrontendDocument
	archive, err := newFrontendArchive(m, raw)
	if err != nil {
		return zero, err
	}
	entry, err := archive.read(m.Presentation.Entry)
	if err != nil {
		return zero, err
	}
	if !utf8.Valid(entry.Content) || !boundedDocumentHTML(entry.Content) {
		return zero, ErrUnsupportedFrontendDocument
	}
	root, err := html.Parse(bytes.NewReader(entry.Content))
	if err != nil {
		return zero, ErrUnsupportedFrontendDocument
	}
	b := frontendDocumentBuilder{archive: archive, encoded: map[string]string{}}
	if err := b.visit(root, m.Presentation.Entry, 0); err != nil {
		return zero, err
	}
	var output boundedDocumentBuffer
	if err := html.Render(&output, root); err != nil {
		return zero, ErrUnsupportedFrontendDocument
	}
	content := output.Bytes()
	sum := sha256.Sum256(content)
	return FrontendDocument{SourceSHA256: m.Frontend.SHA256, DocumentSHA256: hex.EncodeToString(sum[:]), CSP: frontendDocumentCSP, Content: content}, nil
}

// Bound tokenization before the HTML tree builder can allocate pathological trees.
func boundedDocumentHTML(raw []byte) bool {
	tokenizer := html.NewTokenizer(bytes.NewReader(raw))
	tokenizer.SetMaxBuf(MaxFrontendFileBytes)
	count, depth := 0, 0
	for {
		typ := tokenizer.Next()
		count++
		if count > 8192 {
			return false
		}
		if typ == html.ErrorToken {
			return errors.Is(tokenizer.Err(), io.EOF)
		}
		if typ == html.StartTagToken || typ == html.SelfClosingTagToken {
			if !uniqueRawHTMLAttributes(tokenizer.Raw()) {
				return false
			}
			token := tokenizer.Token()
			seen := map[string]bool{}
			for _, attribute := range token.Attr {
				if seen[attribute.Key] {
					return false
				}
				seen[attribute.Key] = true
			}
			if typ == html.StartTagToken {
				switch token.Data {
				case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
				default:
					depth++
				}
			}
		}
		if typ == html.EndTagToken && depth > 0 {
			depth--
		}
		if depth > 64 {
			return false
		}
	}
}

// The tokenizer itself drops duplicate attributes before Token/TagAttr returns.
// Inspect the raw bounded start tag first, retaining only ASCII attribute names.
func uniqueRawHTMLAttributes(raw []byte) bool {
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
	i := 1
	for i < len(raw) && !space(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	seen := map[string]bool{}
	for i < len(raw) {
		for i < len(raw) && space(raw[i]) {
			i++
		}
		if i >= len(raw) || raw[i] == '>' {
			return true
		}
		if raw[i] == '/' {
			return i+1 < len(raw) && raw[i+1] == '>' && i+2 == len(raw)
		}
		start := i
		for i < len(raw) && !space(raw[i]) && raw[i] != '=' && raw[i] != '>' && raw[i] != '/' {
			c := raw[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
				return false
			}
			i++
		}
		if i == start {
			return false
		}
		key := strings.ToLower(string(raw[start:i]))
		if seen[key] || len(seen) >= 128 {
			return false
		}
		seen[key] = true
		for i < len(raw) && space(raw[i]) {
			i++
		}
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && space(raw[i]) {
				i++
			}
			if i >= len(raw) {
				return false
			}
			if raw[i] == '\'' || raw[i] == '"' {
				quote := raw[i]
				i++
				for i < len(raw) && raw[i] != quote {
					i++
				}
				if i >= len(raw) {
					return false
				}
				i++
			} else {
				for i < len(raw) && !space(raw[i]) && raw[i] != '>' {
					i++
				}
			}
		}
	}
	return true
}

type boundedDocumentBuffer struct{ bytes.Buffer }

func (b *boundedDocumentBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxFrontendDocumentBytes-b.Len() {
		return 0, ErrUnsupportedFrontendDocument
	}
	return b.Buffer.Write(p)
}
func (b *boundedDocumentBuffer) WriteString(p string) (int, error) { return b.Write([]byte(p)) }
func (b *boundedDocumentBuffer) WriteByte(p byte) error            { _, err := b.Write([]byte{p}); return err }

func (b *frontendDocumentBuilder) visit(n *html.Node, base string, depth int) error {
	if depth > 64 {
		return ErrUnsupportedFrontendDocument
	}
	if n.Type == html.ElementNode {
		if n.Namespace != "" {
			return ErrUnsupportedFrontendDocument
		}
		switch n.Data {
		case "base", "iframe", "frame", "frameset", "object", "embed", "applet", "portal":
			return ErrUnsupportedFrontendDocument
		}
		seen := map[string]bool{}
		for i := range n.Attr {
			a := &n.Attr[i]
			if seen[a.Key] {
				return ErrUnsupportedFrontendDocument
			}
			seen[a.Key] = true
			if a.Namespace != "" || strings.HasPrefix(a.Key, "on") {
				return ErrUnsupportedFrontendDocument
			}
			switch a.Key {
			case "srcset", "ping", "background", "poster", "data", "action", "formaction", "srcdoc", "integrity", "crossorigin":
				return ErrUnsupportedFrontendDocument
			case "http-equiv":
				return ErrUnsupportedFrontendDocument
			case "charset":
				if !strings.EqualFold(a.Val, "utf-8") {
					return ErrUnsupportedFrontendDocument
				}
			case "style":
				v, err := b.css(a.Val, base)
				if err != nil {
					return err
				}
				a.Val = v
			case "src":
				kind := ""
				switch n.Data {
				case "script":
					kind = "script"
				case "img":
					kind = "image"
				default:
					return ErrUnsupportedFrontendDocument
				}
				v, err := b.resource(base, a.Val, kind)
				if err != nil {
					return err
				}
				a.Val = v
			case "href":
				if n.Data == "a" && strings.HasPrefix(a.Val, "#") {
					continue
				}
				if n.Data != "link" {
					return ErrUnsupportedFrontendDocument
				}
				kind := ""
				switch attrValue(n, "rel") {
				case "stylesheet":
					kind = "style"
				case "icon":
					kind = "image"
				default:
					return ErrUnsupportedFrontendDocument
				}
				v, err := b.resource(base, a.Val, kind)
				if err != nil {
					return err
				}
				a.Val = v
			}
		}
		if n.Data == "script" {
			typ := strings.ToLower(attrValue(n, "type"))
			if typ != "" && typ != "text/javascript" && typ != "application/javascript" {
				return ErrUnsupportedFrontendDocument
			}
			body, err := documentText(n)
			if err != nil {
				return err
			}
			if attrValue(n, "src") != "" {
				if strings.TrimSpace(body) != "" {
					return ErrUnsupportedFrontendDocument
				}
			} else {
				v, err := b.encode("text/javascript; charset=utf-8", []byte(body))
				if err != nil {
					return err
				}
				n.Attr = append(n.Attr, html.Attribute{Key: "src", Val: v})
				for n.FirstChild != nil {
					n.RemoveChild(n.FirstChild)
				}
			}
		}
		if n.Data == "style" {
			body, err := documentText(n)
			if err != nil {
				return err
			}
			body, err = b.css(body, base)
			if err != nil {
				return err
			}
			for n.FirstChild != nil {
				n.RemoveChild(n.FirstChild)
			}
			n.AppendChild(&html.Node{Type: html.TextNode, Data: body})
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if err := b.visit(child, base, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func documentText(n *html.Node) (string, error) {
	var out strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.TextNode {
			return "", ErrUnsupportedFrontendDocument
		}
		out.WriteString(c.Data)
	}
	return out.String(), nil
}
func (b *frontendDocumentBuilder) encode(contentType string, body []byte) (string, error) {
	size := len("data:"+contentType+";base64,") + base64.StdEncoding.EncodedLen(len(body))
	if size > MaxFrontendDocumentBytes-b.used {
		return "", ErrUnsupportedFrontendDocument
	}
	b.used += size
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}
func (b *frontendDocumentBuilder) resource(base, reference, kind string) (string, error) {
	if strings.HasPrefix(reference, "#") && kind == "css-resource" {
		if len(reference) < 2 || strings.ContainsAny(reference, "\"'<>\\\x00\t\n\r ") {
			return "", ErrUnsupportedFrontendDocument
		}
		return reference, nil
	}
	if strings.HasPrefix(reference, "data:") && (kind == "image" || kind == "css-resource") {
		if strings.ContainsAny(reference, "\"'<>\\\x00\t\n\r ") {
			return "", ErrUnsupportedFrontendDocument
		}
		if len(reference) > MaxFrontendDocumentBytes-b.used {
			return "", ErrUnsupportedFrontendDocument
		}
		b.used += len(reference)
		return reference, nil
	}
	u, err := url.Parse(reference)
	if err != nil || reference == "" || strings.ContainsAny(reference, "%\\?#\x00\t\n\r ") || u.IsAbs() || u.Host != "" || strings.HasPrefix(reference, "/") {
		return "", ErrUnsupportedFrontendDocument
	}
	name := path.Join(path.Dir(base), reference)
	if !safeRelativePath(name) {
		return "", ErrUnsupportedFrontendDocument
	}
	key := kind + ":" + name
	if v, ok := b.encoded[key]; ok {
		if len(v) > MaxFrontendDocumentBytes-b.used {
			return "", ErrUnsupportedFrontendDocument
		}
		b.used += len(v)
		return v, nil
	}
	file, err := b.archive.read(name)
	if err != nil {
		return "", ErrUnsupportedFrontendDocument
	}
	ct := file.Metadata.ContentType
	switch kind {
	case "script":
		if ct != "text/javascript; charset=utf-8" || !utf8.Valid(file.Content) {
			return "", ErrUnsupportedFrontendDocument
		}
	case "style":
		if ct != "text/css; charset=utf-8" || !utf8.Valid(file.Content) {
			return "", ErrUnsupportedFrontendDocument
		}
		text, err := b.css(string(file.Content), name)
		if err != nil {
			return "", err
		}
		file.Content = []byte(text)
	case "image":
		if !strings.HasPrefix(ct, "image/") {
			return "", ErrUnsupportedFrontendDocument
		}
	case "css-resource":
		if !strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "font/") {
			return "", ErrUnsupportedFrontendDocument
		}
	default:
		return "", ErrUnsupportedFrontendDocument
	}
	v, err := b.encode(ct, file.Content)
	if err != nil {
		return "", err
	}
	b.encoded[key] = v
	return v, nil
}
func (b *frontendDocumentBuilder) css(source, base string) (string, error) {
	tokens := scanner.New(source)
	var out strings.Builder
	for {
		t := tokens.Next()
		if t.Type == scanner.TokenEOF {
			return out.String(), nil
		}
		if t.Type == scanner.TokenError {
			return "", ErrUnsupportedFrontendDocument
		}
		value := t.Value
		if t.Type == scanner.TokenAtKeyword {
			if strings.Contains(value, "\\") || strings.EqualFold(value, "@import") {
				return "", ErrUnsupportedFrontendDocument
			}
		}
		if t.Type == scanner.TokenFunction && (strings.Contains(value, "\\") || strings.EqualFold(value, "url(")) {
			return "", ErrUnsupportedFrontendDocument
		}
		if t.Type == scanner.TokenURI {
			inner := strings.TrimSpace(value[4 : len(value)-1])
			if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') {
				if inner[len(inner)-1] != inner[0] {
					return "", ErrUnsupportedFrontendDocument
				}
				inner = inner[1 : len(inner)-1]
			}
			v, err := b.resource(base, inner, "css-resource")
			if err != nil {
				return "", err
			}
			value = fmt.Sprintf("url(\"%s\")", v)
		}
		if len(value) > MaxFrontendDocumentBytes-out.Len() {
			return "", ErrUnsupportedFrontendDocument
		}
		out.WriteString(value)
	}
}
