package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/blaze/napi/internal/app"
	"github.com/blaze/napi/internal/batch"
	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/media"
)

func syncCmd() *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "sync <video> <subtitles file | napiprojekt:hash>",
		Short: "Align subtitles made for another release to the video's audio",
		Long: "Converts the subtitles to SRT and aligns them to the video's audio with alass\n" +
			"(downloaded on first use). The subtitles can be a file or a napiprojekt:<hash>\n" +
			"link copied from a movie's page on napiprojekt.pl.",
		Example: `  napi sync Film.mkv Film.inna-wersja.txt
  napi sync Odcinek.mkv napiprojekt:adf4a3563a2b0dac41bdd7327a9345dc`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			video, src := args[0], args[1]
			r := batch.NewRunner(cfg)
			var content []byte
			lang, source, release := cfg.Languages[0], batch.Source("file"), filepath.Base(src)
			if hash, ok := app.NapiLinkHash(src); ok {
				sub, err := app.DownloadNapiHash(cmd.Context(), r, hash)
				if err != nil {
					return err
				}
				content, lang, source, release = sub.Content, sub.Lang, batch.FromNapiOther, "napiprojekt:"+hash
			} else if content, err = os.ReadFile(src); err != nil {
				return err
			}
			def := batch.OutputPath(video, lang, ".srt", cfg.LangSuffix)
			if out != "" && abs(out) != abs(def) {
				return errors.New("-o is not supported with the batch naming rules; rename the result instead")
			}
			if abs(def) == abs(src) {
				return errors.New("the output would overwrite the input subtitles; rename the input first")
			}
			if _, err := os.Stat(def); err == nil && !force {
				return fmt.Errorf("%s exists; use --force", def)
			}
			fmt.Fprintln(os.Stderr, "syncing (reads the whole audio track, this takes a minute or two)...")
			res := r.SyncSubtitles(cmd.Context(), video, content, lang, source, release)
			if res.Err != nil {
				return res.Err
			}
			fmt.Println("Wrote", res.Output)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "accepted only when equal to the default output name")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing subtitles")
	return cmd
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return strings.ToLower(a)
	}
	return p
}

func searchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <video>",
		Short: "List the OpenSubtitles candidates napi would try for a video, best first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.OpenSubtitles.APIKey == "" {
				return errors.New("set openSubtitles.apiKey in napi.json (see \"napi config\")")
			}
			video := args[0]
			q, movie, ranked, err := batch.NewRunner(cfg).Candidates(cmd.Context(), video)
			if movie != nil {
				fmt.Printf("NapiProjekt: %s, imdb tt%07d\n", movie.Name(), movie.IMDbID)
			}
			switch {
			case q.Episode > 0 && q.IMDbID > 0:
				fmt.Printf("Query: series tt%07d S%02dE%02d\n", q.IMDbID, q.Season, q.Episode)
			case q.IMDbID > 0:
				fmt.Printf("Query: imdb tt%07d\n", q.IMDbID)
			case q.Text != "":
				fmt.Printf("Query: %q year %d S%02dE%02d\n", q.Text, q.Year, q.Season, q.Episode)
			}
			if err != nil {
				return err
			}
			fps, _, _ := media.FrameRate(video)
			dur, _ := media.Duration(video)
			fmt.Printf("%d candidates (video %.3f fps, %s)\n", len(ranked), fps, dur.Round(1e9))
			for i, s := range ranked {
				flags := ""
				if s.MovieHashMatch {
					flags += " EXACT"
				}
				if s.AITranslated || s.MachineTranslated {
					flags += " machine"
				}
				if s.HearingImpaired {
					flags += " HI"
				}
				fmt.Printf("%2d. %s %6.3f fps %6d dl  %s%s\n", i+1, s.Language, s.FPS, s.DownloadCount, s.Release, flags)
			}
			return nil
		},
	}
}
