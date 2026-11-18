package oauth

import (
	"embed"
	"html/template"
)

//go:embed consent.html redirect.html
var consentFS embed.FS

var (
	consentTpl  = template.Must(template.ParseFS(consentFS, "consent.html"))
	redirectTpl = template.Must(template.ParseFS(consentFS, "redirect.html"))
)

func consentTemplate() *template.Template { return consentTpl }

func redirectTemplate() *template.Template { return redirectTpl }
