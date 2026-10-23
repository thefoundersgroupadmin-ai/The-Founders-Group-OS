package api

import (
	_ "embed"
	"strings"
)

//go:embed laptop_install.ps1
var windowsLaptopInstaller string

func powershellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
