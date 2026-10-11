package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPrintVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	for _, value := range []string{"dev", "v0.2.1"} {
		version = value
		for _, command := range []string{"version", "--version"} {
			var output bytes.Buffer
			if !printVersion([]string{command}, &output) || output.String() != "Pika "+value+"\n" {
				t.Fatalf("%s: unexpected output %q", command, output.String())
			}
		}
	}
	for _, args := range [][]string{nil, {"download"}, {"version", "extra"}} {
		var output bytes.Buffer
		if printVersion(args, &output) || output.Len() != 0 {
			t.Fatalf("unexpected version handling for %q", args)
		}
	}
}

func TestNormalizeDownloadArgsAllowsURLBeforeFlags(t *testing.T) {
	got, err := normalizeDownloadArgs([]string{"BV1Rc1gBaERq", "--output", "music", "--quality", "hires"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--output", "music", "--quality", "hires", "BV1Rc1gBaERq"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeDownloadArgsRejectsMissingValue(t *testing.T) {
	if _, err := normalizeDownloadArgs([]string{"BV1Rc1gBaERq", "--output"}); err == nil {
		t.Fatal("expected missing flag value error")
	}
}

func TestLoadSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ini")
	content := "[plugins.bilibili]\ncookie = `cookie-value`\n[download]\nplatform_priority = youtubemusic,bilibili\ntimeout_seconds = 42\nproxy = http://127.0.0.1:7890\nmultipart = false\nmultipart_concurrency = 2\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path, "bilibili")
	if err != nil {
		t.Fatal(err)
	}
	if got.cookie != "cookie-value" || got.timeout != 42*time.Second || got.proxy != "http://127.0.0.1:7890" || got.multipart || got.concurrency != 2 {
		t.Fatalf("unexpected settings: %+v", got)
	}
	if want := []string{"youtubemusic", "bilibili"}; !reflect.DeepEqual(got.platformPriority, want) {
		t.Fatalf("priority = %q, want %q", got.platformPriority, want)
	}
}

func TestLoadYouTubeMusicSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ini")
	if err := os.WriteFile(path, []byte("[plugins.youtubemusic]\ncookie = `youtube-cookie`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path, "youtubemusic")
	if err != nil {
		t.Fatal(err)
	}
	if got.cookie != "youtube-cookie" {
		t.Fatalf("cookie = %q, want youtube-cookie", got.cookie)
	}
}

func TestCookieEnvironmentName(t *testing.T) {
	if got := cookieEnvironmentName("youtubemusic"); got != "PIKA_YOUTUBEMUSIC_COOKIE" {
		t.Fatalf("got %q", got)
	}
}

func TestParsePlatformPriority(t *testing.T) {
	got := parsePlatformPriority(" youtube, bilibili, ytm, unknown, bilibili ")
	want := []string{"youtubemusic", "bilibili"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDetectPlatform(t *testing.T) {
	cases := map[string]string{
		"https://www.bilibili.com/video/BV1Rc1gBaERq": "bilibili",
		"https://youtu.be/dQw4w9WgXcQ":                "youtubemusic",
		"Never Gonna Give You Up":                     "",
	}
	for input, want := range cases {
		if got := detectPlatform(input); got != want {
			t.Errorf("detectPlatform(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	if got, want := safeFilename(` A/B:*? "song" `), "A_B___ _song_"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDefaultConfigAndOutputPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PIKA_CONFIG", "")
	t.Setenv("PIKA_DOWNLOAD_OUTPUT_DIR", "")
	if err := os.Mkdir(filepath.Join(home, ".pika"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".pika", "config.ini")
	if err := os.WriteFile(path, []byte("[download]\noutput_dir = ~/Music/Pika\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := commandOptions{quality: "high"}
	s, err := settingsFor(opts, "bilibili")
	if err != nil || s.output != filepath.Join(home, "Music", "Pika") {
		t.Fatalf("config output: %q, %v", s.output, err)
	}
	t.Setenv("PIKA_DOWNLOAD_OUTPUT_DIR", "env-output")
	s, err = settingsFor(opts, "bilibili")
	if err != nil || s.output != "env-output" {
		t.Fatalf("env output: %q, %v", s.output, err)
	}
	opts.output = "."
	s, err = settingsFor(opts, "bilibili")
	if err != nil || s.output != "." {
		t.Fatalf("explicit output: %q, %v", s.output, err)
	}
	opts.configPath = filepath.Join(home, "missing.ini")
	if _, err := settingsFor(opts, "bilibili"); err == nil {
		t.Fatal("explicit missing config must fail")
	}
}
