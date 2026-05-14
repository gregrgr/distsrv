package server

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	texttmpl "text/template"
)

// TestManifestRendersValidXML guards against the html/template regression
// where rendering the plist template with html/template silently escaped
// the leading "<?xml ?>" processing instruction's `<` to `&lt;`, producing
// invalid XML that iOS rejects.
func TestManifestRendersValidXML(t *testing.T) {
	plistFuncs := texttmpl.FuncMap{"xml": xmlEscapeStr}
	tpl, err := texttmpl.New("").Funcs(plistFuncs).ParseFS(webFS, "web/plist/*")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]any{
		"IPAUrl":        "https://example.com/file/1/MyApp.ipa",
		"IconUrl":       "https://example.com/icon/1.png",
		"BundleID":      "com.example.app",
		"BundleVersion": "42",
		// Deliberate special chars that must be XML-escaped:
		"Title": "Foo & <Bar> 中文 \"app\"",
	}
	if err := tpl.ExecuteTemplate(&buf, "manifest.plist.tmpl", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()

	if strings.HasPrefix(out, "&lt;?xml") {
		t.Fatal("first '<' got HTML-escaped — html/template regression")
	}
	if !strings.HasPrefix(out, "<?xml") {
		t.Fatalf("manifest does not start with <?xml; got: %q", out[:32])
	}

	// Must parse as valid XML.
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("manifest is not valid XML: %v\n---output---\n%s", err, out)
		}
	}

	// Title is XML-escaped (Foo &amp; &lt;Bar&gt; ...).
	if !strings.Contains(out, "Foo &amp; &lt;Bar&gt;") {
		t.Errorf("Title special chars not XML-escaped, got snippet around Title: %s",
			extract(out, "<key>title</key>", "</string>"))
	}

	// Standard plist boilerplate must be intact.
	for _, must := range []string{
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"`,
		`<plist version="1.0">`,
		"<key>kind</key>\n                    <string>software-package</string>",
		"<key>bundle-identifier</key>",
		"<string>com.example.app</string>",
		"<key>bundle-version</key>",
		"<string>42</string>",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("manifest missing fragment: %q", must)
		}
	}
}

func TestMobileconfigRendersValidXML(t *testing.T) {
	plistFuncs := texttmpl.FuncMap{"xml": xmlEscapeStr}
	tpl, err := texttmpl.New("").Funcs(plistFuncs).ParseFS(webFS, "web/plist/*")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	data := map[string]any{
		"Host":        "dist.example.com",
		"AppShortID":  "myapp",
		"AppName":     "我的应用 & friends",
		"OrgName":     "ACME <Inc>",
		"OrgSlug":     "acme",
		"PayloadUUID": "abcd-1234",
	}
	if err := tpl.ExecuteTemplate(&buf, "mobileconfig.tmpl", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<?xml") {
		t.Fatalf("mobileconfig does not start with <?xml; got: %q", out[:32])
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("mobileconfig is not valid XML: %v\n---\n%s", err, out)
		}
	}
}

func extract(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return "(not found)"
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j+len(end)]
}
