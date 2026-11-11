package domain

import "testing"

func validCamera() Camera {
	return Camera{
		ID:    "dog",
		Name:  "Living room",
		Video: VideoSource{Device: "REDRAGON Live Camera", Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500},
		Audio: AudioSource{Device: "REDRAGON Live Camera Audio"},
	}
}

func TestCameraValidateOK(t *testing.T) {
	if err := validCamera().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCameraIDRules(t *testing.T) {
	for _, bad := range []CameraID{"", "Dog", "has space", "-lead", "toolongtoolongtoolongtoolongtoolong"} {
		if err := bad.Validate(); err == nil {
			t.Errorf("id %q should be rejected", bad)
		}
	}
	for _, good := range []CameraID{"dog", "cam-2", "living_room", "a"} {
		if err := good.Validate(); err != nil {
			t.Errorf("id %q should be accepted: %v", good, err)
		}
	}
}

func TestCameraValidateCollectsAllErrors(t *testing.T) {
	c := validCamera()
	c.Video = VideoSource{}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"video.device", "width/height", "fps", "bitrate_kbps"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestHasAudio(t *testing.T) {
	c := validCamera()
	if !c.HasAudio() {
		t.Error("expected audio")
	}
	c.Audio.Device = ""
	if c.HasAudio() {
		t.Error("expected no audio")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestVariants(t *testing.T) {
	c := validCamera()
	v := c.Variants()
	if len(v) != 1 || v[0].Quality != QualityHigh || v[0].Name != "dog" || v[0].Width != 1280 {
		t.Errorf("variants without low = %+v", v)
	}
	c.Video.Low = &LowRes{Width: 640, Height: 360}
	v = c.Variants()
	if len(v) != 2 || v[1].Quality != QualityLow || v[1].Name != "dog_low" || v[1].Width != 640 {
		t.Errorf("variants with low = %+v", v)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("low variant should validate: %v", err)
	}
	c.Video.Low = &LowRes{Width: 1920, Height: 1080}
	if err := c.Validate(); err == nil {
		t.Error("low wider than main must be rejected")
	}
	c.Video.Low = &LowRes{}
	if err := c.Validate(); err == nil {
		t.Error("zero low size must be rejected")
	}
}
