package server

import "embed"

//go:embed web/templates/*.html web/static/* web/plist/*
var webFS embed.FS

// appleIntermediates holds Apple's public WWDR intermediate CA certs
// (DER-encoded .cer files downloaded from www.apple.com/certificateauthority).
// They're concatenated into the PKCS7 chain when signing the UDID-
// collection mobileconfig with an Apple Developer leaf cert — otherwise
// iOS reports "尚未验证 / 无效的描述文件" because the chain back to
// Apple Root CA is broken.
//
//go:embed certs/apple/*.cer
var appleIntermediates embed.FS
