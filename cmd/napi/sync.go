package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/blazejp83/napi/internal/app"
	"github.com/blazejp83/napi/internal/batch"
	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/media"
	"github.com/blazejp83/napi/internal/osdb"
)

func syncCmd() *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "sync <subtitles>... | sync <video> <subtitles file | napiprojekt:hash>",
		Short: "Align subtitles to a video's audio",
		Long: "Converts the subtitles to SRT and aligns them to the video's audio with alass\n" +
			"(downloaded on first use).\n\n" +
			"With only subtitle files, each is re-synced to the video of the same name in its\n" +
			"folder (Film.pol.srt -> Film.mkv) and saved in place; the old file is kept as .bak.\n" +
			"With a video and subtitles (a file or a napiprojekt:<hash> link from a movie's page\n" +
			"on napiprojekt.pl), the result is saved as the video's subtitles.",
		Example: `  napi sync Film.pol.srt
  napi sync D:\Seriale\*.pol.srt
  napi sync Film.mkv Film.inna-wersja.txt
  napi sync Odcinek.mkv napiprojekt:adf4a3563a2b0dac41bdd7327a9345dc`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			if resyncArgs(args) {
				return resync(cmd, cfg, args)
			}
			if len(args) != 2 {
				return errors.New("give subtitle files to re-sync, or one video and one subtitle file/link")
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
			def := batch.OutputPath(video, lang, ".srt", cfg.Naming)
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

func accountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "account",
		Short: "Check the OpenSubtitles login and today's remaining downloads",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			o := cfg.OpenSubtitles
			if o.APIKey == "" {
				return errors.New("set openSubtitles.apiKey in napi.json (see \"napi config\")")
			}
			info, err := osdb.NewClient(o.APIKey, o.Username, o.Password, o.UserAgent).UserInfo(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("OpenSubtitles: %s, %s, %d of %d downloads left today\n", o.Username, info.Level, info.RemainingDownloads, info.AllowedDownloads)
			return nil
		},
	}
}

// resyncArgs reports whether every argument is a subtitle file.
func resyncArgs(args []string) bool {
	for _, a := range args {
		if _, ok := app.NapiLinkHash(a); ok || !batch.IsSubtitleFile(a) {
			return false
		}
	}
	return true
}

func resync(cmd *cobra.Command, cfg config.Config, args []string) error {
	// cmd.exe passes wildcards through unexpanded.
	var files []string
	for _, a := range args {
		if strings.ContainsAny(a, "*?") {
			m, _ := filepath.Glob(a)
			files = append(files, m...)
		} else {
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		return errors.New("no subtitle files match")
	}
	r := batch.NewRunner(cfg)
	fmt.Fprintln(os.Stderr, "syncing (each file reads its video's whole audio track)...")
	r.Progress = func(done, total int, res batch.Result) {
		msg := "-> " + filepath.Base(res.Output)
		if res.Err != nil {
			msg = res.Err.Error()
		}
		fmt.Printf("[%d/%d] %-9s %s  %s\n", done, total, res.Status, filepath.Base(res.Video), msg)
	}
	s := batch.Summarize(r.SyncFiles(cmd.Context(), files))
	fmt.Printf("\nSynced %d, errors %d\n", s.Synced, s.Failed)
	if s.Failed > 0 {
		return exitError{1}
	}
	return nil
}
