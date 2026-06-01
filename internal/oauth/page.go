package oauth

import (
	_ "embed"
	"io"
	"text/template"
)

var (
	//go:embed page.html
	page string

	browserResponseTmpl = template.Must(template.New("browser").Parse(page))
)

type browserState struct {
	Title, Body string
	Success     bool
}

func renderPage(w io.Writer, state browserState) {
	_ = browserResponseTmpl.Execute(w, state)
}
