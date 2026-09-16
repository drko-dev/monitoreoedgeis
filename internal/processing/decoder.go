package processing

import "errors"

// errDecoderClosed is returned by Push once the decoder has been closed.
var errDecoderClosed = errors.New("processing: decoder is closed")

// VideoDecoder decodes H.264 access units into raw video frames. It is
// stream-oriented (Push/Frames), not request/response, because the real
// implementation (ffmpeg, run as a subprocess) is a continuous process, not
// a per-call API — see ffmpeg_decoder.go for why a subprocess instead of a
// cgo binding or a pure-Go decoder.
type VideoDecoder interface {
	// Push feeds one access unit for decoding. It does not wait for the
	// corresponding frame(s) to come out.
	Push(au AccessUnit) error
	// Frames returns the channel of decoded frames, open for the decoder's
	// lifetime. Its buffer is deliberately small (raw yuv420p frames are
	// large); a slow drainer causes frames to be dropped, not memory growth
	// — see FFmpegDecoder's DroppedCount.
	Frames() <-chan DecodedFrame
	// Done is closed when the decoder process exits, whether via Close or
	// unexpectedly (crash). Callers use it to detect when a replacement
	// decoder is needed.
	Done() <-chan struct{}
	// DecodedCount returns the number of frames decoded so far.
	DecodedCount() int64
	// DroppedCount returns the number of decoded frames dropped because
	// Frames() wasn't drained in time (a backpressure drop, not a decode
	// failure).
	DroppedCount() int64
	// Close stops the decoder and releases its resources. Safe to call more
	// than once.
	Close() error
}
