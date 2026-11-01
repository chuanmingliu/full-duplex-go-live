package focus

import "testing"

func TestExactPrimaryDurationRespectsMasksOverlapAndClipping(t *testing.T) {
	tr := NewTracker(Config{})
	tr.Pin(0)
	tr.Mask(0, 400)
	tr.Observe(Frame{StartMS: 0, EndMS: 400, P: []float32{1, 0}})
	tr.Observe(Frame{StartMS: 400, EndMS: 1000, P: []float32{0, 1}})
	tr.Observe(Frame{StartMS: 1000, EndMS: 1200, P: []float32{1, 1}})
	if a := tr.Attribute(200, 1200); a.PrimaryMS != 200 || a.VoicedMS != 800 {
		t.Fatalf("masked primary counted: %+v", a)
	}
	if a := tr.AttributeOver(200, 1200); a.PrimaryMS != 400 || a.VoicedMS != 1000 {
		t.Fatalf("clipped frame or overlapping speakers miscounted: %+v", a)
	}
	if a := tr.AttributeOver(250, 350); a.PrimaryMS != 100 || a.VoicedMS != 100 {
		t.Fatalf("partial frame lost exact duration: %+v", a)
	}
	if a := tr.AttributeOver(1200, 1200); a.PrimaryMS != 0 || a.VoicedMS != 0 {
		t.Fatalf("empty span has duration: %+v", a)
	}
}

func TestExactPrimaryDurationExcludesEcho(t *testing.T) {
	tr := NewTracker(Config{})
	tr.Pin(0)
	tr.Mask(0, 1000)
	tr.Observe(Frame{StartMS: 0, EndMS: 1000, P: []float32{0, 1}})
	tr.Observe(Frame{StartMS: 1000, EndMS: 1400, P: []float32{1, 0}})
	if tr.Echo() != 1 {
		t.Fatal("test did not identify the assistant echo")
	}
	if a := tr.AttributeOver(0, 1400); a.PrimaryMS != 400 || a.VoicedMS != 400 {
		t.Fatalf("echo counted as caller or other speech: %+v", a)
	}
}
