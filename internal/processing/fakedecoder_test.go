package processing

import (
	"sync"
	"sync/atomic"
	"time"
)

// fakeDecoder is a VideoDecoder that turns each pushed AccessUnit into one
// DecodedFrame synchronously, with no external process — used by
// pipeline_test.go and manager_test.go to exercise the pipeline end-to-end
// without spawning ffmpeg (that path is separately covered, ffmpeg-gated,
// in decoder_test.go).
type fakeDecoder struct {
	width, height int
	frames        chan DecodedFrame
	done          chan struct{}
	closeOnce     sync.Once

	decoded atomic.Int64
	dropped atomic.Int64

	// failPush, if set, makes every Push return an error (simulating a
	// dead decoder) without closing done itself — the caller is expected
	// to trigger Done() separately if needed.
	failPush atomic.Bool
}

func newFakeDecoder(width, height int) *fakeDecoder {
	return &fakeDecoder{
		width:  width,
		height: height,
		frames: make(chan DecodedFrame, 8),
		done:   make(chan struct{}),
	}
}

func (f *fakeDecoder) Push(au AccessUnit) error {
	if f.failPush.Load() {
		return errDecoderClosed
	}
	f.decoded.Add(1)
	frame := DecodedFrame{
		Data:             make([]byte, f.width*f.height*3/2),
		Width:            f.width,
		Height:           f.height,
		PipelineSeq:      uint64(f.decoded.Load()),
		SourceReceivedAt: au.ReceivedAt,
		DecodedAt:        time.Now(),
	}
	select {
	case f.frames <- frame:
	default:
		f.dropped.Add(1)
	}
	return nil
}

func (f *fakeDecoder) Frames() <-chan DecodedFrame { return f.frames }
func (f *fakeDecoder) Done() <-chan struct{}       { return f.done }
func (f *fakeDecoder) DecodedCount() int64         { return f.decoded.Load() }
func (f *fakeDecoder) DroppedCount() int64         { return f.dropped.Load() }

func (f *fakeDecoder) Close() error {
	f.closeOnce.Do(func() { close(f.done) })
	return nil
}

// crash makes the decoder look dead: further Push calls fail, and Done()
// closes as if the process had exited unexpectedly.
func (f *fakeDecoder) crash() {
	f.failPush.Store(true)
	f.closeOnce.Do(func() { close(f.done) })
}
