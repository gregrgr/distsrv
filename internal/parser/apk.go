package parser

import (
	"bytes"
	"fmt"
	"image/png"

	"github.com/shogo82148/androidbinary"
	"github.com/shogo82148/androidbinary/apk"
)

type APKInfo struct {
	Package     string
	VersionName string
	VersionCode string
	Label       string
	IconBytes   []byte
}

func ParseAPK(p string) (*APKInfo, error) {
	pkg, err := apk.OpenFile(p)
	if err != nil {
		return nil, fmt.Errorf("open apk: %w", err)
	}
	defer pkg.Close()

	m := pkg.Manifest()

	info := &APKInfo{
		Package:     pkg.PackageName(),
		VersionName: m.VersionName.MustString(),
		VersionCode: fmt.Sprintf("%d", m.VersionCode.MustInt32()),
	}

	if label, err := pkg.Label(nil); err == nil {
		info.Label = label
	}

	// Try high-density icon first, fall back to default.
	cfg := &androidbinary.ResTableConfig{Density: 320}
	if img, err := pkg.Icon(cfg); err == nil && img != nil {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil {
			info.IconBytes = buf.Bytes()
		}
	}
	if info.IconBytes == nil {
		if img, err := pkg.Icon(nil); err == nil && img != nil {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err == nil {
				info.IconBytes = buf.Bytes()
			}
		}
	}

	return info, nil
}
