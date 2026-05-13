package parser

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"howett.net/plist"
)

type IPAInfo struct {
	BundleID      string
	BundleVersion string // CFBundleVersion
	ShortVersion  string // CFBundleShortVersionString
	DisplayName   string
	Title         string // 优先 DisplayName，否则 BundleName
	IconBytes     []byte // 已尽可能取最大的一张
}

type infoPlist struct {
	BundleID      string                 `plist:"CFBundleIdentifier"`
	BundleVersion string                 `plist:"CFBundleVersion"`
	ShortVersion  string                 `plist:"CFBundleShortVersionString"`
	BundleName    string                 `plist:"CFBundleName"`
	DisplayName   string                 `plist:"CFBundleDisplayName"`
	IconFiles     []string               `plist:"CFBundleIconFiles"`
	IconsDict     map[string]interface{} `plist:"CFBundleIcons"`
}

// ParseIPA reads the .ipa zip from path and extracts metadata + best icon.
func ParseIPA(p string) (*IPAInfo, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("open ipa zip: %w", err)
	}
	defer zr.Close()

	var infoFile *zip.File
	var appDir string
	for _, f := range zr.File {
		if matched, _ := path.Match("Payload/*.app/Info.plist", f.Name); matched {
			infoFile = f
			appDir = path.Dir(f.Name)
			break
		}
	}
	if infoFile == nil {
		return nil, fmt.Errorf("Info.plist not found inside ipa (expected Payload/*.app/Info.plist)")
	}

	rc, err := infoFile.Open()
	if err != nil {
		return nil, fmt.Errorf("open Info.plist: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read Info.plist: %w", err)
	}

	var info infoPlist
	if _, err := plist.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode Info.plist: %w", err)
	}

	out := &IPAInfo{
		BundleID:      info.BundleID,
		BundleVersion: info.BundleVersion,
		ShortVersion:  info.ShortVersion,
		DisplayName:   info.DisplayName,
	}
	if info.DisplayName != "" {
		out.Title = info.DisplayName
	} else {
		out.Title = info.BundleName
	}

	iconNames := collectIconNames(&info)
	if len(iconNames) > 0 {
		out.IconBytes = pickBestIcon(zr.File, appDir, iconNames)
	}
	return out, nil
}

func collectIconNames(info *infoPlist) []string {
	set := map[string]struct{}{}
	for _, n := range info.IconFiles {
		set[n] = struct{}{}
	}
	for _, key := range []string{"CFBundlePrimaryIcon", "CFBundleAlternateIcons"} {
		raw, ok := info.IconsDict[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case map[string]interface{}:
			collectFromIconDict(v, set)
		case []interface{}:
			for _, item := range v {
				if m, ok := item.(map[string]interface{}); ok {
					collectFromIconDict(m, set)
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	return names
}

func collectFromIconDict(m map[string]interface{}, set map[string]struct{}) {
	if files, ok := m["CFBundleIconFiles"].([]interface{}); ok {
		for _, item := range files {
			if s, ok := item.(string); ok && s != "" {
				set[s] = struct{}{}
			}
		}
	}
	if name, ok := m["CFBundleIconName"].(string); ok && name != "" {
		set[name] = struct{}{}
	}
}

// pickBestIcon enumerates icon files inside the .app dir matching any of iconNames,
// picks the largest by zip uncompressed size as a heuristic for highest resolution.
func pickBestIcon(files []*zip.File, appDir string, iconNames []string) []byte {
	var candidates []*zip.File
	for _, f := range files {
		if !strings.HasPrefix(f.Name, appDir+"/") {
			continue
		}
		base := path.Base(f.Name)
		if !strings.HasSuffix(strings.ToLower(base), ".png") {
			continue
		}
		matched := false
		for _, n := range iconNames {
			if strings.HasPrefix(base, n) {
				matched = true
				break
			}
		}
		if matched {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		// fallback: any png directly in the .app dir
		for _, f := range files {
			if path.Dir(f.Name) == appDir && strings.HasSuffix(strings.ToLower(f.Name), ".png") {
				candidates = append(candidates, f)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].UncompressedSize64 > candidates[j].UncompressedSize64
	})
	rc, err := candidates[0].Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil
	}
	return buf.Bytes()
}
