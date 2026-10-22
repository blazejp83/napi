// Command napi downloads subtitles from napiprojekt.pl for video files and folders.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/blaze/napi/internal/app"
	"github.com/blaze/napi/internal/batch"
	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/media"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/subs"
	"github.com/blaze/napi/internal/winui"
)

// exitError carries a process exit code through cobra.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := rootCmd().ExecuteContext(ctx)
	var ee exitError
	switch {
	case errors.As(err, &ee):
		os.Exit(ee.code)
	case err != nil:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	get := getCmd()
	root := &cobra.Command{
		Use:           "napi [file or folder...]",
		Short:         "Download subtitles from napiprojekt.pl",
		Long:          "napi finds subtitles on napiprojekt.pl for video files and whole folders.\nWith paths and no command it runs \"napi get\".",
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return get.RunE(get, args)
		},
	}
	root.Flags().AddFlagSet(get.Flags())
	root.AddCommand(get, hashCmd(), fetchCmd(), syncCmd(), searchCmd(), accountCmd(), configCmd(), menuCmd())
	return root
}

type getFlags struct {
	langs              string
	format, encoding   string
	naming             string
	force              bool
	noRecursive, quiet bool
	jobs               int
	fps                float64
	report             string
}

func getCmd() *cobra.Command {
	var f getFlags
	cmd := &cobra.Command{
		Use:   "get <file or folder>...",
		Short: "Download subtitles for videos and folders",
		Example: `  napi get Film.mkv
  napi get D:\Filmy E:\Seriale --lang PL,ENG --report raport.csv`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("lang") {
				cfg.Languages = strings.Split(f.langs, ",")
			}
			if fl.Changed("format") {
				cfg.Format = f.format
			}
			if fl.Changed("encoding") {
				cfg.Encoding = f.encoding
			}
			if fl.Changed("naming") {
				cfg.Naming = f.naming
			}
			if fl.Changed("force") {
				cfg.Overwrite = f.force
			}
			if fl.Changed("no-recursive") {
				cfg.Recursive = !f.noRecursive
			}
			if fl.Changed("jobs") {
				cfg.Jobs = f.jobs
			}
			if fl.Changed("fps") {
				cfg.DefaultFPS = f.fps
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			return runGet(cmd.Context(), args, cfg, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.langs, "lang", "l", "PL", "languages to try in order: PL, ENG or PL,ENG")
	fl.StringVar(&f.format, "format", "srt", "output format: srt (convert) or original")
	fl.StringVarP(&f.encoding, "encoding", "e", subs.EncUTF8BOM, "output encoding: utf8bom, utf8 or cp1250")
	fl.StringVarP(&f.naming, "naming", "n", config.NamingISO3, "file names: iso639-2 (Film.pol.srt), iso639-1 (Film.pl.srt) or none (Film.srt)")
	fl.BoolVarP(&f.force, "force", "f", false, "replace existing subtitles in that language (old ones are kept as .bak)")
	fl.BoolVar(&f.noRecursive, "no-recursive", false, "do not descend into subfolders")
	fl.IntVarP(&f.jobs, "jobs", "j", 4, "videos processed in parallel")
	fl.Float64Var(&f.fps, "fps", 23.976, "frame rate for MicroDVD subtitles when the video's cannot be read")
	fl.StringVarP(&f.report, "report", "r", "", "write a CSV report to this file")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "print only the summary")
	return cmd
}

func runGet(ctx context.Context, args []string, cfg config.Config, f getFlags) error {
	videos, err := batch.Collect(args, cfg)
	if err != nil {
		return err
	}
	if len(videos) == 0 {
		return errors.New("no video files found")
	}
	runner := batch.NewRunner(cfg)
	width := len(fmt.Sprint(len(videos)))
	runner.Progress = func(done, total int, r batch.Result) {
		if f.quiet && r.Status != batch.Failed {
			return
		}
		fmt.Printf("[%*d/%d] %-9s %s  %s\n", width, done, total, r.Status, displayPath(r.Video, args), app.Describe(r))
	}
	results := runner.Run(ctx, videos)
	app.Remember(results)
	s := batch.Summarize(results)
	fmt.Printf("\nDownloaded %d, already had %d, not found %d, errors %d\n", s.Downloaded, s.Skipped, s.NotFound, s.Failed)
	if f.report != "" {
		if err := app.WriteCSV(f.report, results); err != nil {
			return fmt.Errorf("report: %w", err)
		}
		fmt.Println("Report:", f.report)
	}
	if code := s.ExitCode(); code != 0 {
		return exitError{code}
	}
	return nil
}

// displayPath shortens a video path relative to the folder argument it came from.
func displayPath(video string, roots []string) string {
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(abs, video); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(video)
}

func hashCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hash <file>...",
		Short: "Print the NapiProjekt hash and frame rate of video files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, p := range args {
				h, err := napi.FileHash(p)
				if err != nil {
					return err
				}
				fps := "?"
				if v, src, err := media.FrameRate(p); err == nil {
					fps = fmt.Sprintf("%.3f (%s)", v, src)
				}
				fmt.Printf("%s  fps %s  %s\n", h, fps, p)
			}
			return nil
		},
	}
}

func fetchCmd() *cobra.Command {
	var lang, out, format, encoding string
	var fps float64
	cmd := &cobra.Command{
		Use:   "fetch <hash>",
		Short: "Download subtitles by NapiProjekt hash (for testing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			sub, err := napi.NewClient(cfg.Client).Download(cmd.Context(), strings.ToLower(args[0]), strings.ToUpper(lang))
			if err != nil {
				return err
			}
			text, charset := subs.Decode(sub.Content)
			detected := subs.Detect(text)
			fmt.Fprintf(os.Stderr, "found: %s, %s, %d bytes, uploader %s\n", detected, charset, len(sub.Content), sub.Uploader)
			if format == "srt" && detected != subs.Unknown {
				if d := subs.DeclaredFPS(text); d > 0 {
					fps = d
				}
				cues, err := subs.Parse(text, detected, fps)
				if err != nil {
					return err
				}
				text = subs.WriteSRT(cues)
			}
			data := subs.Encode(text, strings.ToLower(encoding))
			if out == "" || out == "-" {
				_, err = os.Stdout.Write(data)
				return err
			}
			return os.WriteFile(out, data, 0o644)
		},
	}
	cmd.Flags().StringVarP(&lang, "lang", "l", "PL", "PL or ENG")
	cmd.Flags().StringVarP(&out, "output", "o", "", "output file (default stdout)")
	cmd.Flags().StringVar(&format, "format", "srt", "srt or original")
	cmd.Flags().StringVarP(&encoding, "encoding", "e", subs.EncUTF8, "utf8bom, utf8 or cp1250")
	cmd.Flags().Float64Var(&fps, "fps", 23.976, "frame rate for MicroDVD conversion")
	return cmd
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the settings file and current settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, p, err := config.Load()
			if err != nil {
				return err
			}
			if _, err := os.Stat(p); err != nil {
				fmt.Printf("# %s (not created yet, showing defaults; run \"napi config init\")\n", p)
			} else {
				fmt.Printf("# %s\n", p)
			}
			return cfgJSON(cfg)
		},
	}
	var portable bool
	init := &cobra.Command{
		Use:   "init",
		Short: "Write a settings file with the current values",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, p, err := config.Load()
			if err != nil {
				return err
			}
			if portable {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				p = filepath.Join(filepath.Dir(exe), config.FileName)
			}
			if err := cfg.Save(p); err != nil {
				return err
			}
			fmt.Println("Wrote", p)
			return nil
		},
	}
	init.Flags().BoolVar(&portable, "portable", false, "write napi.json next to the executable")
	cmd.AddCommand(init)
	return cmd
}

func cfgJSON(cfg config.Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func menuCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "menu",
		Short: "Add or remove \"Pobierz napisy\" in the Windows Explorer right-click menu",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Add the Explorer menu entry for video files and folders (current user, no admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			exe, err := winui.MenuExecutable()
			if err != nil {
				return err
			}
			if err := winui.InstallMenu(exe, cfg.Extensions); err != nil {
				return err
			}
			fmt.Println("Installed. Right-click a video or folder → \"Pobierz napisy\" or \"Pobierz napisy (eng)\" (Windows 11: Show more options).")
			fmt.Println("napiprojekt: links on napiprojekt.pl now open napi (for this user; uninstall restores the official app).")
			fmt.Println("Command:", exe)
			return nil
		},
	}, &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Explorer menu entry",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := winui.UninstallMenu(); err != nil {
				return err
			}
			fmt.Println("Removed.")
			return nil
		},
	})
	return cmd
}
