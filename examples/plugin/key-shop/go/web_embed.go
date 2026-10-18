package main

import "embed"

//go:embed web/admin.html web/dashboard.html web/vendor/flatpickr.min.css web/vendor/flatpickr.min.js
var webFS embed.FS
