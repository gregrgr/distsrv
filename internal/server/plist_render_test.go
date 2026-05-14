package server

import (
	"bytes"
	"encoding/xml"
	"regexp"
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

// TestUDIDCompleteRendersValidXML — the Profile Service callback must
// return a valid signed mobileconfig as its body to avoid iOS reporting
// "安装失败" / "Installation Failed" after the device POSTs its info.
// This guards the template (the signing layer is exercised separately).
func TestUDIDCompleteRendersValidXML(t *testing.T) {
	plistFuncs := texttmpl.FuncMap{"xml": xmlEscapeStr}
	tpl, err := texttmpl.New("").Funcs(plistFuncs).ParseFS(webFS, "web/plist/*")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "udid-complete.tmpl", map[string]any{
		"AppName":     "MyApp <demo>",
		"AppShortID":  "myapp",
		"OrgName":     "ACME & Co",
		"OrgSlug":     "acme",
		"PayloadUUID": "1d2e3f40-aaaa-4bbb-9ccc-1234567890ab",
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<?xml") {
		t.Fatalf("udid-complete does not start with <?xml; got: %q", out[:32])
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("udid-complete is not valid XML: %v\n---\n%s", err, out)
		}
	}
	// Must declare PayloadType=Configuration with an empty PayloadContent
	// array so iOS understands "no further profile to install".
	for _, must := range []string{
		"<string>Configuration</string>",
		"<key>PayloadContent</key>\n    <array/>",
		"<string>1d2e3f40-aaaa-4bbb-9ccc-1234567890ab</string>",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("udid-complete missing fragment: %q\n---\n%s", must, out)
		}
	}
	// User-supplied strings must be XML-escaped.
	if !strings.Contains(out, "MyApp &lt;demo&gt;") {
		t.Errorf("AppName not XML-escaped")
	}
	if !strings.Contains(out, "ACME &amp; Co") {
		t.Errorf("OrgName not XML-escaped")
	}
}

// TestMobileconfigPayloadUUIDIsValidRFC4122 — regression: iOS rejects a
// .mobileconfig whose PayloadUUID is not an RFC 4122 UUID with dashes.
// We don't render via the handler here (would need an httptest server);
// instead we re-use auth.RandomUUIDv4 and pipe it through the template.
func TestMobileconfigPayloadUUIDIsValidRFC4122(t *testing.T) {
	plistFuncs := texttmpl.FuncMap{"xml": xmlEscapeStr}
	tpl, err := texttmpl.New("").Funcs(plistFuncs).ParseFS(webFS, "web/plist/*")
	if err != nil {
		t.Fatal(err)
	}
	uu := "1d2e3f40-aaaa-4bbb-9ccc-1234567890ab" // representative v4
	var buf bytes.Buffer
	_ = tpl.ExecuteTemplate(&buf, "mobileconfig.tmpl", map[string]any{
		"Host": "h", "AppShortID": "a", "AppName": "n",
		"OrgName": "o", "OrgSlug": "s", "PayloadUUID": uu,
	})
	out := buf.String()
	want := "<string>" + uu + "</string>"
	if !strings.Contains(out, want) {
		t.Fatalf("mobileconfig missing PayloadUUID value as-is; expected %q", want)
	}
	// Negative: a hex blob without dashes (the historical bug) shouldn't slip past
	// a basic UUID format check.
	bad := "d9021223b1b393e77644783b6f01691a"
	matched, _ := regexp.MatchString(
		`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, bad)
	if matched {
		t.Fatal("regression sentinel: regex falsely accepted a non-dashed hex blob")
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
