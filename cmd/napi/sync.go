package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/media"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/osdb"
	"github.com/blaze/napi/internal/subs"
	"github.com/blaze/napi/internal/subsync"
)

func syncCmd() *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "sync <video> <subtitles>",
		Short: "Align subtitles made for another release to the video's audio",
		Long: "Converts the subtitles to SRT and aligns them to the video's audio with alass\n" +
			"(downloaded on first use). Use it for subtitles fetched by hand, e.g. from napiprojekt.pl.",
		Example: `  napi sync Film.mkv Film.inna-wersja.txt
  napi sync Film.mkv napisy.srt -o Film.pl.srt`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			video, subFile := args[0], args[1]
			raw, err := os.ReadFile(subFile)
			if err != nil {
				return err
			}
			text, charset := subs.Decode(raw)
			format := subs.Detect(text)
			if format == subs.Unknown {
				return errors.New("unknown subtitle format")
			}
			fps := 0.0
			if format == subs.MicroDVD {
				if fps = subs.DeclaredFPS(text); fps == 0 {
					if fps, _, err = media.FrameRate(video); err != nil {
						fps = cfg.DefaultFPS
					}
				}
			}
			cues, err := subs.Parse(text, format, fps)
			if err != nil {
				return err
			}
			if out == "" {
				out = strings.TrimSuffix(video, filepath.Ext(video)) + ".srt"
			}
			if abs(out) == abs(subFile) {
				return errors.New("output would overwrite the input; use -o")
			}
			if _, err := os.Stat(out); err == nil && !force {
				return fmt.Errorf("%s exists; use --force or -o", out)
			}
			fmt.Fprintf(os.Stderr, "input: %s, %s, %d lines\n", format, charset, len(cues))
			tools, err := subsync.Ensure(cmd.Context(), cfg.AlassPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "syncing (reads the whole audio track, this takes a minute or two)...")
			synced, err := tools.Sync(cmd.Context(), video, subs.WriteSRT(cues))
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, subs.Encode(synced, cfg.Encoding), 0o644); err != nil {
				return err
			}
			fmt.Println("Wrote", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output file (default: video name with .srt)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite the output file")
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
		Short: "List OpenSubtitles candidates for a video, best first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.OpenSubtitles.APIKey == "" {
				return errors.New("set openSubtitles.apiKey in napi.json (see \"napi config\")")
			}
			video, ctx := args[0], cmd.Context()
			o := cfg.OpenSubtitles
			client := osdb.NewClient(o.APIKey, o.Username, o.Password, o.UserAgent)
			var langs []string
			for _, l := range cfg.Languages {
				if l == "ENG" {
					l = "en"
				}
				langs = append(langs, strings.ToLower(l))
			}
			q := osdb.Query{Languages: langs}
			if h, _, err := osdb.FileHash(video); err == nil {
				q.MovieHash = h
			}
			if nh, err := napi.FileHash(video); err == nil {
				if m, err := napi.NewClient(cfg.Client).MovieInfo(ctx, nh); err == nil {
					fmt.Printf("NapiProjekt: %s (%d) imdb tt%07d\n", m.Title, m.Year, m.IMDbID)
					q.IMDbID = m.IMDbID
				}
			}
			if q.IMDbID == 0 {
				q.Text, q.Year = osdb.GuessTitle(video)
				fmt.Printf("Searching by title: %q %d\n", q.Text, q.Year)
			}
			hits, err := client.Search(ctx, q)
			if err != nil {
				return err
			}
			fps, _, _ := media.FrameRate(video)
			ranked := osdb.Rank(hits, filepath.Base(video), fps, langs)
			fmt.Printf("%d candidates (video fps %.3f)\n", len(ranked), fps)
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
