package app

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/blazejp83/napi/internal/batch"
	"github.com/blazejp83/napi/internal/napi"
)

var reNapiLink = regexp.MustCompile(`(?i)^napiprojekt:(?://)?([0-9a-f]{32})/?$`)

// NapiLinkHash extracts the hash from a napiprojekt:<hash> link, the form
// napiprojekt.pl uses for each subtitle version on a movie's page.
func NapiLinkHash(s string) (string, bool) {
	m := reNapiLink.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1]), true
}

// DownloadNapiHash fetches the subtitles behind a napiprojekt: link in the
// first configured language that has them.
func DownloadNapiHash(ctx context.Context, r *batch.Runner, hash string) (*napi.Subtitles, error) {
	var err error
	for _, lang := range r.Config.Languages {
		var sub *napi.Subtitles
		if sub, err = r.Client.Download(ctx, hash, lang); err == nil {
			return sub, nil
		}
		if !errors.Is(err, napi.ErrNotFound) {
			return nil, err
		}
	}
	return nil, err
}
