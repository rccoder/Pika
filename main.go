package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/rccoder/Pika/internal/music/bilibili"
	"github.com/rccoder/Pika/internal/music/download"
	"github.com/rccoder/Pika/internal/music/platform"
	"github.com/rccoder/Pika/internal/music/youtubemusic"
	"gopkg.in/ini.v1"
)

const usage = `Pika downloads playable audio from Bilibili and YouTube Music.

Usage:
  pika download <Bilibili/YouTube URL, ID, or search keyword> [options]
  pika youtube download <YouTube Music URL or video ID> [options]

Options:
  --output, -o DIR       destination directory (default: .)
  --quality QUALITY      standard, high, lossless, or hires (default: high)
  --config FILE          optional INI configuration file
  --cookie VALUE         Cookie for an explicit platform URL (prefer env/file)
  --cookie-file FILE     file containing a platform Cookie
  --timeout SECONDS      download timeout (default: 90)
  --proxy URL            HTTP/SOCKS proxy for media downloads
`

type settings struct {
	cookie           string
	output           string
	quality          platform.Quality
	timeout          time.Duration
	proxy            string
	multipart        bool
	concurrency      int
	platformPriority []string
}

type commandOptions struct {
	input, output, quality, configPath string
	cookie, cookieFile, proxy          string
	timeoutSeconds                     int
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Fprint(os.Stdout, usage)
		return
	}
	var err error
	switch os.Args[1] {
	case "download":
		err = runAutoDownload(os.Args[2:])
	case "youtube":
		if len(os.Args) < 3 || os.Args[2] != "download" {
			fatalf("usage: pika youtube download <YouTube Music URL or video ID> [options]")
		}
		err = runDownload(os.Args[3:], "youtubemusic")
	default:
		fatalf("unknown command %q\n\n%s", os.Args[1], usage)
	}
	if err != nil {
		fatalf("download failed: %v", err)
	}
}

func runDownload(args []string, platformName string) error {
	opts, err := parseCommandOptions(args)
	if err != nil {
		return err
	}
	s, err := settingsFor(opts, platformName)
	if err != nil {
		return err
	}
	return downloadWithPlatform(opts.input, platformName, s)
}

func parseCommandOptions(args []string) (commandOptions, error) {
	args, err := normalizeDownloadArgs(args)
	if err != nil {
		return commandOptions{}, err
	}
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("output", "", "destination directory (overrides environment and config)")
	fs.StringVar(output, "o", "", "destination directory")
	qualityValue := fs.String("quality", "high", "audio quality")
	configPath := fs.String("config", os.Getenv("PIKA_CONFIG"), "INI configuration file")
	cookieFlag := fs.String("cookie", "", "platform Cookie")
	cookieFile := fs.String("cookie-file", "", "file containing a platform Cookie")
	timeoutSeconds := fs.Int("timeout", 0, "download timeout in seconds")
	proxy := fs.String("proxy", "", "download proxy URL")
	if err := fs.Parse(args); err != nil {
		return commandOptions{}, err
	}
	if fs.NArg() != 1 {
		return commandOptions{}, errors.New("download requires exactly one URL, ID, or search keyword")
	}
	return commandOptions{input: fs.Arg(0), output: *output, quality: *qualityValue, configPath: *configPath, cookie: *cookieFlag, cookieFile: *cookieFile, proxy: *proxy, timeoutSeconds: *timeoutSeconds}, nil
}

func settingsFor(opts commandOptions, platformName string) (settings, error) {
	q, err := platform.ParseQuality(strings.ToLower(strings.TrimSpace(opts.quality)))
	if err != nil || q == platform.QualityAtmos {
		return settings{}, fmt.Errorf("unsupported quality %q (use standard, high, lossless, or hires)", opts.quality)
	}
	fileSettings, err := loadSettings(opts.configPath, platformName)
	if err != nil {
		return settings{}, err
	}
	cookie, err := chooseCookie(opts.cookie, opts.cookieFile, fileSettings.cookie)
	if err != nil {
		return settings{}, err
	}
	if value := strings.TrimSpace(os.Getenv(cookieEnvironmentName(platformName))); value != "" && strings.TrimSpace(opts.cookie) == "" && strings.TrimSpace(opts.cookieFile) == "" {
		cookie = value
	}
	if opts.timeoutSeconds > 0 {
		fileSettings.timeout = time.Duration(opts.timeoutSeconds) * time.Second
	}
	if value := strings.TrimSpace(os.Getenv("PIKA_DOWNLOAD_TIMEOUT_SECONDS")); value != "" && opts.timeoutSeconds == 0 {
		if seconds, parseErr := time.ParseDuration(value + "s"); parseErr == nil && seconds > 0 {
			fileSettings.timeout = seconds
		}
	}
	if strings.TrimSpace(opts.proxy) != "" {
		fileSettings.proxy = opts.proxy
	}
	if value := strings.TrimSpace(os.Getenv("PIKA_DOWNLOAD_PROXY")); value != "" && strings.TrimSpace(opts.proxy) == "" {
		fileSettings.proxy = value
	}
	if value := strings.TrimSpace(os.Getenv("PIKA_DOWNLOAD_OUTPUT_DIR")); value != "" {
		fileSettings.output = value
	}
	if opts.output != "" {
		fileSettings.output = opts.output
	}
	fileSettings.output, err = expandHomePath(fileSettings.output)
	if err != nil {
		return settings{}, err
	}
	fileSettings.cookie, fileSettings.quality = cookie, q
	return fileSettings, nil
}

func downloadWithPlatform(raw, platformName string, s settings) error {
	switch platformName {
	case "bilibili":
		return downloadBilibili(raw, s)
	case "youtubemusic":
		if s.quality > platform.QualityHigh {
			return errors.New("YouTube Music supports only standard and high quality")
		}
		return downloadYouTubeMusic(raw, s)
	default:
		return fmt.Errorf("unsupported platform %q", platformName)
	}
}

// runAutoDownload routes an explicit URL/ID to its platform. For plain search
// text, it searches configured platforms in priority order and probes stream
// metadata before downloading, so a high-quality result wins over a standard
// one from a higher-priority platform.
func runAutoDownload(args []string) error {
	opts, err := parseCommandOptions(args)
	if err != nil {
		return err
	}
	if platformName := detectPlatform(opts.input); platformName != "" {
		s, err := settingsFor(opts, platformName)
		if err != nil {
			return err
		}
		return downloadWithPlatform(opts.input, platformName, s)
	}
	if strings.TrimSpace(opts.cookie) != "" || strings.TrimSpace(opts.cookieFile) != "" {
		return errors.New("--cookie and --cookie-file require a recognized URL; use platform-specific environment variables or config for keyword search")
	}
	base, err := settingsFor(opts, "bilibili")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var fallback *searchCandidate
	for _, platformName := range base.platformPriority {
		s, settingsErr := settingsFor(opts, platformName)
		if settingsErr != nil {
			continue
		}
		if platformName == "youtubemusic" && s.quality > platform.QualityHigh {
			continue
		}
		provider := newPlatformProvider(platformName, s)
		if provider == nil {
			continue
		}
		tracks, searchErr := provider.Search(ctx, opts.input, 5)
		if searchErr != nil || len(tracks) == 0 {
			continue
		}
		for _, track := range tracks {
			info, infoErr := provider.GetDownloadInfo(ctx, track.ID, s.quality)
			if infoErr != nil || info == nil || strings.TrimSpace(info.URL) == "" {
				continue
			}
			candidate := &searchCandidate{platformName: platformName, provider: provider, track: track, info: info, settings: s}
			if info.Quality >= platform.QualityHigh {
				return downloadResolvedTrack(ctx, candidate.platformName, &candidate.track, candidate.info, candidate.settings)
			}
			if fallback == nil || info.Quality > fallback.info.Quality {
				fallback = candidate
			}
		}
	}
	if fallback != nil {
		return downloadResolvedTrack(ctx, fallback.platformName, &fallback.track, fallback.info, fallback.settings)
	}
	return fmt.Errorf("no downloadable result found for %q", opts.input)
}

type searchCandidate struct {
	platformName string
	provider     platform.Platform
	track        platform.Track
	info         *platform.DownloadInfo
	settings     settings
}

func detectPlatform(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := bilibili.NewURLMatcher().MatchURL(raw); ok {
		return "bilibili"
	}
	if _, ok := bilibili.NewURLMatcher().MatchText(raw); ok {
		return "bilibili"
	}
	if _, ok := youtubemusic.NewURLMatcher().MatchURL(raw); ok {
		return "youtubemusic"
	}
	if _, ok := youtubemusic.NewTextMatcher().MatchText(raw); ok {
		return "youtubemusic"
	}
	return ""
}

func newPlatformProvider(platformName string, s settings) platform.Platform {
	switch platformName {
	case "bilibili":
		return bilibili.NewPlatform(bilibili.New(nil, s.cookie, "", false, 0, nil), 5)
	case "youtubemusic":
		return youtubemusic.NewPlatform(youtubemusic.NewClient(s.cookie, s.timeout, nil))
	default:
		return nil
	}
}

// normalizeDownloadArgs permits the conventional `download URL --output DIR`
// form even though Go's flag package otherwise stops parsing at the first URL.
func normalizeDownloadArgs(args []string) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	needsValue := map[string]bool{
		"--output": true, "-o": true, "--quality": true, "--config": true,
		"--cookie": true, "--cookie-file": true, "--timeout": true, "--proxy": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if needsValue[arg] {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...), nil
}

func loadSettings(path, platformName string) (settings, error) {
	s := settings{output: ".", timeout: 90 * time.Second, multipart: true, concurrency: 4, platformPriority: []string{"bilibili", "youtubemusic"}}
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return s, err
		}
		path = filepath.Join(home, ".pika", "config.ini")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return s, nil
		}
	}
	path, err := expandHomePath(path)
	if err != nil {
		return s, err
	}
	cfg, err := ini.Load(path)
	if err != nil {
		return s, fmt.Errorf("read config %q: %w", path, err)
	}
	s.cookie = strings.Trim(cfg.Section("plugins."+platformName).Key("cookie").String(), "`\"'")
	if s.cookie == "" {
		s.cookie = strings.Trim(cfg.Section(platformName).Key("cookie").String(), "`\"'")
	}
	downloadCfg := cfg.Section("download")
	if value := strings.TrimSpace(downloadCfg.Key("output_dir").String()); value != "" {
		s.output = value
	}
	if raw := strings.TrimSpace(downloadCfg.Key("platform_priority").String()); raw != "" {
		s.platformPriority = parsePlatformPriority(raw)
	}
	if seconds, err := downloadCfg.Key("timeout_seconds").Int(); err == nil && seconds > 0 {
		s.timeout = time.Duration(seconds) * time.Second
	}
	if value := strings.TrimSpace(downloadCfg.Key("proxy").String()); value != "" {
		s.proxy = value
	}
	if value, err := downloadCfg.Key("multipart").Bool(); err == nil {
		s.multipart = value
	}
	if value, err := downloadCfg.Key("multipart_concurrency").Int(); err == nil && value > 0 {
		s.concurrency = value
	}
	return s, nil
}

func expandHomePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func parsePlatformPriority(raw string) []string {
	seen := make(map[string]struct{})
	priority := make([]string, 0, 2)
	for _, item := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(item))
		if name == "youtube" || name == "ytm" {
			name = "youtubemusic"
		}
		if name != "bilibili" && name != "youtubemusic" {
			continue
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			priority = append(priority, name)
		}
	}
	if len(priority) == 0 {
		return []string{"bilibili", "youtubemusic"}
	}
	return priority
}

func cookieEnvironmentName(platformName string) string {
	if platformName == "youtubemusic" {
		return "PIKA_YOUTUBEMUSIC_COOKIE"
	}
	return "PIKA_BILIBILI_COOKIE"
}

func chooseCookie(flagValue, cookieFile, configValue string) (string, error) {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value, nil
	}
	if path := strings.TrimSpace(cookieFile); path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read cookie file: %w", err)
		}
		return strings.TrimSpace(string(content)), nil
	}
	return strings.TrimSpace(configValue), nil
}

func downloadBilibili(raw string, s settings) error {
	trackID, ok := bilibili.NewURLMatcher().MatchURL(strings.TrimSpace(raw))
	if !ok {
		trackID, ok = bilibili.NewURLMatcher().MatchText(strings.TrimSpace(raw))
	}
	if !ok {
		return errors.New("not a supported Bilibili URL or ID")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := bilibili.New(nil, s.cookie, "", false, 0, nil)
	defer client.Close()
	provider := bilibili.NewPlatform(client, 5)
	return downloadPlatformTrack(ctx, "bilibili", provider, trackID, s)
}

func downloadYouTubeMusic(raw string, s settings) error {
	trackID, ok := youtubemusic.NewURLMatcher().MatchURL(strings.TrimSpace(raw))
	if !ok {
		trackID, ok = youtubemusic.NewTextMatcher().MatchText(strings.TrimSpace(raw))
	}
	if !ok {
		return errors.New("not a supported YouTube or YouTube Music URL or video ID")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	provider := youtubemusic.NewPlatform(youtubemusic.NewClient(s.cookie, s.timeout, nil))
	return downloadPlatformTrack(ctx, "youtubemusic", provider, trackID, s)
}

func downloadPlatformTrack(ctx context.Context, platformName string, provider platform.Platform, trackID string, s settings) error {
	track, err := provider.GetTrack(ctx, trackID)
	if err != nil {
		return fmt.Errorf("get track: %w", err)
	}
	info, err := provider.GetDownloadInfo(ctx, trackID, s.quality)
	if err != nil {
		return fmt.Errorf("get download URL: %w", err)
	}
	return downloadResolvedTrack(ctx, platformName, track, info, s)
}

func downloadResolvedTrack(ctx context.Context, platformName string, track *platform.Track, info *platform.DownloadInfo, s settings) error {
	if track == nil || info == nil || strings.TrimSpace(info.URL) == "" {
		return errors.New("download information unavailable")
	}
	if err := os.MkdirAll(s.output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	name := safeFilename(trackArtistNames(track) + " - " + track.Title)
	if name == "" {
		name = platformName + "-" + safeFilename(track.ID)
	}
	staged, err := os.CreateTemp(s.output, ".pika-*.download")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	stagePath := staged.Name()
	if err := staged.Close(); err != nil {
		return err
	}
	defer os.Remove(stagePath)
	fmt.Printf("Downloading %s (%s)…\n", track.Title, info.Quality.String())
	downloadService := download.NewDownloadService(download.DownloadServiceOptions{Timeout: s.timeout, Proxy: s.proxy, MaxRetries: 3, EnableMultipart: s.multipart, MultipartConcurrency: s.concurrency, MultipartMinSize: 5 << 20})
	var last int64
	_, err = downloadService.Download(ctx, info, stagePath, func(written, total int64) {
		if written-last < 5<<20 && total > 0 && written < total {
			return
		}
		last = written
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\\r%.1f%%", float64(written)*100/float64(total))
		} else {
			fmt.Fprintf(os.Stderr, "\\r%d MiB", written>>20)
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	ext := playableExtension(info.Format)
	finalPath := availablePath(filepath.Join(s.output, name+"."+ext))
	if err := remuxAudio(ctx, stagePath, finalPath, ext); err != nil {
		return err
	}
	fmt.Printf("Saved: %s\n", finalPath)
	return nil
}

func playableExtension(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "flac":
		return "flac"
	case "opus":
		return "opus"
	case "webm":
		return "webm"
	case "mp3":
		return "mp3"
	default:
		return "m4a"
	}
}

func remuxAudio(ctx context.Context, source, destination, ext string) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errors.New("ffmpeg is required to create a playable m4a/flac file; install ffmpeg and retry")
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-i", source, "-vn", "-map", "0:a:0", "-c:a", "copy", destination)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remux to %s: %w: %s", ext, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func availablePath(path string) string {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func safeFilename(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '_'
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Trim(strings.Join(strings.Fields(value), " "), ". ")
	if len([]rune(value)) > 180 {
		value = string([]rune(value)[:180])
	}
	return value
}

func trackArtistNames(track *platform.Track) string {
	if track == nil {
		return ""
	}
	names := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
