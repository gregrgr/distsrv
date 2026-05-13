package server

import "embed"

//go:embed web/templates/*.html web/static/* web/plist/*
var webFS embed.FS
