package oauth

import (
	"embed"
	"html/template"
)

//go:embed consent.html
var consentFS embed.FS

var consentTpl = template.Must(template.ParseFS(consentFS, "consent.html"))

func consentTemplate() *template.Template { return consentTpl }
