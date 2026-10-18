package main

import "embed"

//go:embed web/admin.html web/dashboard.html
var webFS embed.FS
