package upgrade

import "testing"

func TestDecide_ResolutionMismatch(t *testing.T) {
	incoming := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 4_000_000}
	existing := FileInfo{Codec: "hevc", Width: 1280, Height: 720, BitrateBps: 4_000_000}

	if d, _ := Decide(incoming, existing, 0.25); d != Replace {
		t.Errorf("higher-res incoming: got %v, want Replace", d)
	}
	if d, _ := Decide(existing, incoming, 0.25); d != Keep {
		t.Errorf("higher-res existing: got %v, want Keep", d)
	}
}

func TestDecide_CodecTier(t *testing.T) {
	incoming := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 4_000_000}
	existing := FileInfo{Codec: "h264", Width: 1920, Height: 1080, BitrateBps: 4_000_000}

	if d, _ := Decide(incoming, existing, 0.25); d != Replace {
		t.Errorf("hevc over h264 at equal res: got %v, want Replace", d)
	}
	if d, _ := Decide(existing, incoming, 0.25); d != Keep {
		t.Errorf("h264 incoming vs hevc existing: got %v, want Keep", d)
	}
}

func TestDecide_Bitrate(t *testing.T) {
	incoming := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 6_000_000}
	existing := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 4_000_000}

	if d, _ := Decide(incoming, existing, 0.25); d != Replace {
		t.Errorf("50%% higher bitrate at equal res/codec: got %v, want Replace", d)
	}

	// Within threshold — too close to call.
	close := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 4_500_000}
	if d, _ := Decide(close, existing, 0.25); d != Review {
		t.Errorf("bitrate within threshold: got %v, want Review", d)
	}

	// A lower incoming bitrate (e.g. a re-encode with a better shrink ratio) is
	// never auto-discarded — quality could be equal or better despite the size
	// drop, so it always needs a manual look rather than an auto-Keep.
	lower := FileInfo{Codec: "hevc", Width: 1920, Height: 1080, BitrateBps: 2_000_000}
	if d, _ := Decide(lower, existing, 0.25); d != Review {
		t.Errorf("lower incoming bitrate at equal res/codec: got %v, want Review", d)
	}
}

func TestDecide_Ambiguous(t *testing.T) {
	incoming := FileInfo{Codec: "", Width: 0, Height: 0, BitrateBps: 0}
	existing := FileInfo{Codec: "", Width: 0, Height: 0, BitrateBps: 0}

	if d, _ := Decide(incoming, existing, 0.25); d != Review {
		t.Errorf("no comparable data: got %v, want Review", d)
	}
}
