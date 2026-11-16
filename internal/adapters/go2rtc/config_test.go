package go2rtc

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/maikomr/watchmydog/internal/domain"
)

func testSettings() Settings {
	return Settings{
		Bin: "go2rtc", APIAddr: "127.0.0.1:1984", RTSPAddr: "127.0.0.1:8554", WebRTCPort: 8555,
		FFmpegBin: "/opt/homebrew/bin/ffmpeg", CaptureBin: "/Users/x/.local/bin/wmd-capture", LogLevel: "info",
	}
}

func dogCamera() domain.Camera {
	return domain.Camera{
		ID: "dog", Name: "Living room",
		Video: domain.VideoSource{Device: "REDRAGON Live Camera", Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500},
		Audio: domain.AudioSource{Device: "REDRAGON Live Camera Audio"},
	}
}

func TestRenderConfigIsValidYAMLWithExpectedSources(t *testing.T) {
	out, err := RenderConfig(testSettings(), []domain.Camera{dogCamera()})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		API     struct{ Listen string }      `yaml:"api"`
		WebRTC  struct{ Listen string }      `yaml:"webrtc"`
		FFmpeg  struct{ Bin string }         `yaml:"ffmpeg"`
		Streams map[string][]string          `yaml:"streams"`
		Log     struct{ Level, Exec string } `yaml:"log"`
	}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("generated config is not valid YAML: %v\n%s", err, out)
	}
	if parsed.API.Listen != "127.0.0.1:1984" || parsed.WebRTC.Listen != ":8555" || parsed.FFmpeg.Bin != "/opt/homebrew/bin/ffmpeg" {
		t.Errorf("top-level settings wrong: %+v", parsed)
	}
	srcs := parsed.Streams["dog"]
	if len(srcs) != 2 {
		t.Fatalf("dog should have 2 sources, got %v", srcs)
	}
	// Audio comes first so audio-only viewers do not start the camera.
	audio, video := srcs[0], srcs[1]
	for _, want := range []string{
		"exec:sh -c \"", "'/Users/x/.local/bin/wmd-capture' run --device 'REDRAGON Live Camera'",
		"--width 1280 --height 720 --fps 30", "'/opt/homebrew/bin/ffmpeg'",
		"-video_size 1280x720 -framerate 30", "-b:v 2500k -g 60", "-f rtsp {output}", "#killsignal=15#killtimeout=5",
	} {
		if !strings.Contains(video, want) {
			t.Errorf("video source missing %q:\n%s", want, video)
		}
	}
	if audio != "ffmpeg:device?audio=REDRAGON+Live+Camera+Audio#audio=opus" {
		t.Errorf("audio source = %q", audio)
	}
}

func TestRenderConfigNoAudioAndNoCameras(t *testing.T) {
	c := dogCamera()
	c.Audio.Device = ""
	out, err := RenderConfig(testSettings(), []domain.Camera{c})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Streams map[string][]string `yaml:"streams"`
	}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Streams["dog"]) != 1 {
		t.Errorf("expected only the video source, got %v", parsed.Streams["dog"])
	}

	out, err = RenderConfig(testSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var empty struct {
		Streams map[string][]string `yaml:"streams"`
	}
	if err := yaml.Unmarshal(out, &empty); err != nil {
		t.Fatalf("empty streams must still be valid YAML: %v\n%s", err, out)
	}
	if len(empty.Streams) != 0 {
		t.Errorf("expected no streams, got %v", empty.Streams)
	}
}

func TestRenderConfigIsDeterministic(t *testing.T) {
	a := dogCamera()
	b := dogCamera()
	b.ID = "cat"
	out1, _ := RenderConfig(testSettings(), []domain.Camera{a, b})
	out2, _ := RenderConfig(testSettings(), []domain.Camera{b, a})
	if string(out1) != string(out2) {
		t.Error("camera order must not change the output")
	}
}

func TestShellQuoting(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
	if got := shellQuoteDouble(`say "hi"`); got != `"say \"hi\""` {
		t.Errorf("shellQuoteDouble = %s", got)
	}
}

func TestRenderConfigLowVariant(t *testing.T) {
	c := dogCamera()
	c.Video.Low = &domain.LowRes{Width: 640, Height: 360}
	out, err := RenderConfig(testSettings(), []domain.Camera{c})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Streams map[string][]string `yaml:"streams"`
	}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	low := parsed.Streams["dog_low"]
	if len(low) != 1 {
		t.Fatalf("low stream = %v", low)
	}
	for _, want := range []string{
		"exec:/opt/homebrew/bin/ffmpeg", "-i rtsp://127.0.0.1:8554/dog", "-vf scale=640:-2",
		"-c:v h264_videotoolbox", "-b:v 625k", "-g 60", "-c:a copy", "-f rtsp {output}#killsignal=15",
	} {
		if !strings.Contains(low[0], want) {
			t.Errorf("low stream missing %q: %s", want, low[0])
		}
	}
	c.Audio.Device = ""
	out, _ = RenderConfig(testSettings(), []domain.Camera{c})
	if !strings.Contains(string(out), " -an -f rtsp") {
		t.Errorf("low stream without audio should disable audio:\n%s", out)
	}
}
