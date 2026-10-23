package gui

import (
	"html"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/blazejp83/napi/internal/napi"
)

// searchPage submits napiprojekt.pl's search form as soon as it loads.
const searchPage = `<!doctype html>
<html lang="pl"><head><meta charset="utf-8"><title>Napi – szukam „%[1]s”</title>
<style>body{font:15px "Segoe UI",sans-serif;margin:3em;color:#1b1b1f}button{font:inherit;padding:.4em 1em}</style></head>
<body onload="document.forms[0].submit()">
<form method="post" action="%[2]s"><input type="hidden" name="searchInp" value="%[1]s">
<p>Szukam „%[1]s” na napiprojekt.pl…</p>
<p>Na liście wyników wybierz film, potem <b>napisy</b>, i kliknij wybraną wersję – Napi dopasuje ją do Twojego pliku.</p>
<noscript><button type="submit">Szukaj</button></noscript></form></body></html>`

func openSearchPage(title string) error {
	p := filepath.Join(os.TempDir(), "napi-szukaj.html")
	t := html.EscapeString(title)
	page := []byte(strings.NewReplacer("%[1]s", t, "%[2]s", napi.SearchURL).Replace(searchPage))
	if err := os.WriteFile(p, page, 0o644); err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
