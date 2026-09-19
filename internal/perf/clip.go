package perf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClipSpec fully describes the synthetic video source a decode measurement
// runs on. Every field is recorded in the report, because a decode FPS number
// without its codec/resolution/frame rate is not reproducible.
//
// The default is deliberately modest and camera-like rather than flattering:
// 640x360 is this repo's own DefaultVideoOutputWidth/Height, 15 fps is a
// typical camera sub-stream rate, and 150 frames is 10 seconds — long enough
// to amortise process startup, short enough to iterate.
type ClipSpec struct {
	Width  int
	Height int
	FPS    float64
	Frames int
	// GOP is the IDR interval in frames. A GOP of 30 at 15 fps means a key
	// frame every 2 s, matching a realistic camera sub-stream.
	GOP int
	// LavfiSource is the deterministic lavfi generator. testsrc2 is used
	// because it is fully synthetic (no input file, no network) and contains
	// both motion and hard edges, so it is not a degenerate all-flat frame
	// that would make decode or JPEG encoding unrepresentatively cheap.
	LavfiSource string
}

// DefaultClipSpec returns the spec the documentation's numbers were measured
// with.
func DefaultClipSpec() ClipSpec {
	return ClipSpec{
		Width:  640,
		Height: 360,
		FPS:    15,
		// 20 seconds: long enough that priming and the tail flush are a small
		// part of a run, so the measured window can be a genuine steady state
		// rather than a warming decoder.
		Frames:      300,
		GOP:         30,
		LavfiSource: "testsrc2",
	}
}

// ParseClipSpec parses a "WxH@FPSxFRAMES" override (e.g. "1280x720@15x300").
// Any omitted component keeps its DefaultClipSpec value, so "1280x720" alone
// is valid. It exists so an operator can re-run the same harness at a
// different resolution without recompiling, not so the documented default can
// be quietly weakened.
func ParseClipSpec(raw string) (ClipSpec, error) {
	spec := DefaultClipSpec()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return spec, nil
	}
	rest := raw
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		frames := rest[i+1:]
		rest = rest[:i]
		if j := strings.IndexByte(frames, 'x'); j >= 0 {
			f, err := strconv.Atoi(frames[j+1:])
			if err != nil || f <= 0 {
				return spec, fmt.Errorf("perf: bad frame count %q", frames[j+1:])
			}
			spec.Frames = f
			frames = frames[:j]
		}
		fps, err := strconv.ParseFloat(frames, 64)
		if err != nil || fps <= 0 {
			return spec, fmt.Errorf("perf: bad FPS %q", frames)
		}
		spec.FPS = fps
	}
	if i := strings.IndexByte(rest, 'x'); i >= 0 {
		w, err1 := strconv.Atoi(rest[:i])
		h, err2 := strconv.Atoi(rest[i+1:])
		if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
			return spec, fmt.Errorf("perf: bad resolution %q", rest)
		}
		spec.Width, spec.Height = w, h
	} else if rest != "" {
		return spec, fmt.Errorf("perf: bad clip spec %q (want WxH@FPSxFRAMES)", raw)
	}
	if spec.Width%2 != 0 || spec.Height%2 != 0 {
		return spec, fmt.Errorf("perf: %dx%d is not even; yuv420p requires even dimensions", spec.Width, spec.Height)
	}
	return spec, nil
}

// NominalDuration is the clip's intended duration at its nominal frame rate.
func (s ClipSpec) NominalDuration() time.Duration {
	return time.Duration(float64(s.Frames) / s.FPS * float64(time.Second))
}

// FFmpegArgs returns the exact ffmpeg argument vector used to generate the
// clip. It is recorded verbatim in the report so the source is reproducible
// by copy-paste rather than by trusting this doc comment.
func (s ClipSpec) FFmpegArgs(outPath string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		// bitexact suppresses encoder version/identification metadata, which
		// is what makes two generations on the same host byte-identical.
		"-fflags", "+bitexact",
		"-f", "lavfi",
		"-i", fmt.Sprintf("%s=size=%dx%d:rate=%g", s.LavfiSource, s.Width, s.Height, s.FPS),
		"-frames:v", strconv.Itoa(s.Frames),
		"-r", strconv.FormatFloat(s.FPS, 'g', -1, 64),
		"-c:v", "libx264",
		"-flags:v", "+bitexact",
		"-preset", "veryfast",
		// zerolatency disables B-frames and lookahead: decoded output then
		// follows push order, which is what makes the pipeline's FIFO
		// SourceReceivedAt correlation meaningful while measuring.
		"-tune", "zerolatency",
		"-profile:v", "main",
		"-g", strconv.Itoa(s.GOP),
		"-keyint_min", strconv.Itoa(s.GOP),
		"-sc_threshold", "0",
		"-pix_fmt", "yuv420p",
		// aud=1 emits an access unit delimiter per picture (the boundary this
		// harness relies on); repeat-headers keeps SPS/PPS in-band at every
		// IDR, like a real camera; no-scenecut keeps the GOP structure fixed.
		"-x264-params", "no-scenecut=1:repeat-headers=1:aud=1",
		"-f", "h264",
		outPath,
	}
}

// ClipInfo is the frozen description of the video source a measurement ran
// on: what it is, how it was made, and what it hashes to.
type ClipInfo struct {
	Path             string  `json:"path"`
	SHA256           string  `json:"sha256"`
	Bytes            int64   `json:"bytes"`
	Container        string  `json:"container"`
	Codec            string  `json:"codec"`
	Profile          string  `json:"profile"`
	PixelFormat      string  `json:"pix_fmt"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	NominalSourceFPS float64 `json:"nominal_source_fps"`
	// NominalSourceFPSSource says where NominalSourceFPS came from. A raw
	// Annex-B elementary stream carries no container timing, so ffprobe
	// *guesses*; the authoritative value is the generator's -r argument.
	NominalSourceFPSSource string `json:"nominal_source_fps_source"`
	// FFProbeGuessedFPS is ffprobe's r_frame_rate for the raw stream, kept
	// only so the guess is visible and cannot be mistaken for the nominal
	// rate. Not used in any calculation.
	FFProbeGuessedFPS string `json:"ffprobe_guessed_fps,omitempty"`
	CodedFrames       int64  `json:"coded_frames"`
	// AccessUnitBoundary records which rule the harness used to derive access
	// units from the byte stream: "aud" (H.264 access unit delimiters, exact
	// for multi-slice pictures) or "vcl-fallback" (one access unit per slice,
	// which is only valid for single-slice pictures). A "vcl-fallback" source
	// is accepted only because VerifyAccessUnitCount agreed with ffprobe.
	AccessUnitBoundary string  `json:"access_unit_boundary"`
	MeasuredDurationS  float64 `json:"nominal_duration_s"`
	GenerationCommand  string  `json:"generation_command"`
	FFmpegVersion      string  `json:"ffmpeg_version"`
	FFprobeVersion     string  `json:"ffprobe_version,omitempty"`
	// RegeneratedIdentical is true when generating the same spec twice on this
	// host produced byte-identical output. False means the clip is still a
	// valid, documented source, but not bit-reproducible across runs of this
	// particular ffmpeg/x264 build.
	RegeneratedIdentical bool `json:"regenerated_identical"`
}

// GenerateClip renders spec to outPath with ffmpeg and returns the frozen
// ClipInfo. It fails rather than substituting a different source: a
// measurement whose input silently changed is worse than no measurement.
func GenerateClip(ctx context.Context, ffmpegPath string, spec ClipSpec, outPath string) (ClipInfo, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if _, err := exec.LookPath(ffmpegPath); err != nil {
		return ClipInfo{}, fmt.Errorf("perf: ffmpeg %q not found (needed only to generate the benchmark clip): %w", ffmpegPath, err)
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ClipInfo{}, err
		}
	}
	args := spec.FFmpegArgs(outPath)
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ClipInfo{}, fmt.Errorf("perf: ffmpeg clip generation failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	info, err := os.Stat(outPath)
	if err != nil {
		return ClipInfo{}, err
	}
	sum, err := fileSHA256(outPath)
	if err != nil {
		return ClipInfo{}, err
	}

	ffmpegVersion := firstLine(commandOutput(ctx, ffmpegPath, "-hide_banner", "-version"))

	return ClipInfo{
		Path:                   outPath,
		SHA256:                 sum,
		Bytes:                  info.Size(),
		Container:              "raw H.264 Annex-B elementary stream (-f h264)",
		Codec:                  "h264",
		Width:                  spec.Width,
		Height:                 spec.Height,
		NominalSourceFPS:       spec.FPS,
		NominalSourceFPSSource: fmt.Sprintf("ffmpeg generator -r %g (authoritative; raw Annex-B carries no container timing)", spec.FPS),
		CodedFrames:            int64(spec.Frames),
		MeasuredDurationS:      spec.NominalDuration().Seconds(),
		GenerationCommand:      strings.Join(append([]string{ffmpegPath}, args...), " "),
		FFmpegVersion:          ffmpegVersion,
	}, nil
}

// ClipProbe mirrors the small subset of `ffprobe -print_format json` this
// harness reads.
type ClipProbe struct {
	Streams []ClipProbeStream `json:"streams"`
}

// ClipProbeStream is one probed video stream.
type ClipProbeStream struct {
	CodecName    string `json:"codec_name"`
	Profile      string `json:"profile"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	PixFmt       string `json:"pix_fmt"`
	RFrameRate   string `json:"r_frame_rate"`
	AvgFrameRate string `json:"avg_frame_rate"`
	NbReadFrames string `json:"nb_read_frames"`
}

// ProbeClip returns the independently observed properties of a raw H.264
// file: the video stream's real codec/profile/dimensions and, critically, the
// coded-frame count obtained with -count_frames. VerifyAccessUnitCount uses
// that count to refuse measuring when the harness's own access-unit split
// disagrees with the decoder's view of the stream.
func ProbeClip(ctx context.Context, ffprobePath, clipPath string) (stream ClipProbe, err error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	if _, err := exec.LookPath(ffprobePath); err != nil {
		return stream, fmt.Errorf("perf: ffprobe %q not found (needed to verify the benchmark clip): %w", ffprobePath, err)
	}
	out, err := commandOutputErr(ctx, ffprobePath,
		"-hide_banner", "-loglevel", "error",
		"-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,width,height,pix_fmt,r_frame_rate,avg_frame_rate,nb_read_frames",
		"-print_format", "json",
		clipPath,
	)
	if err != nil {
		return stream, fmt.Errorf("perf: ffprobe failed: %w", err)
	}
	if err := json.Unmarshal(out, &stream); err != nil {
		return stream, fmt.Errorf("perf: ffprobe output not understood: %w", err)
	}
	if len(stream.Streams) == 0 {
		return stream, fmt.Errorf("perf: ffprobe found no video stream in %s", clipPath)
	}
	return stream, nil
}

// ProbeCodedFrames returns only the independent coded-frame count.
func ProbeCodedFrames(ctx context.Context, ffprobePath, clipPath string) (int64, error) {
	res, err := ProbeClip(ctx, ffprobePath, clipPath)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(res.Streams[0].NbReadFrames), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("perf: ffprobe reported an unusable frame count %q (does this ffprobe support -count_frames?): %w",
			res.Streams[0].NbReadFrames, err)
	}
	return n, nil
}

// ApplyProbe copies the independently observed stream properties into info,
// leaving the harness's own generator-derived fields (nominal FPS, spec) in
// place and recording ffprobe's guess alongside them for transparency.
func (c ClipInfo) ApplyProbe(res ClipProbe) ClipInfo {
	s := res.Streams[0]
	if s.CodecName != "" {
		c.Codec = s.CodecName
	}
	if s.Profile != "" {
		c.Profile = s.Profile
	}
	if s.PixFmt != "" {
		c.PixelFormat = s.PixFmt
	}
	if s.Width > 0 {
		c.Width = s.Width
	}
	if s.Height > 0 {
		c.Height = s.Height
	}
	if s.RFrameRate != "" {
		c.FFProbeGuessedFPS = s.RFrameRate
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(s.NbReadFrames), 10, 64); err == nil {
		c.CodedFrames = n
	}
	return c
}

// VerifyAndParse reads the clip file this ClipInfo describes, cross-checks
// the harness's own access-unit split against ffprobe's independent coded
// frame count, and returns the parsed access units. It fails rather than
// returning a number derived from the wrong denominator, and it preserves
// every generation-related field already set (sha256, command, version) so it
// can be applied to a ClipInfo produced by GenerateClip.
func (c ClipInfo) VerifyAndParse(ctx context.Context, ffprobePath string) (ClipInfo, []AccessUnit, error) {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return c, nil, err
	}
	aus, sawAUD := GroupAccessUnits(SplitAnnexB(raw))
	if len(aus) == 0 {
		return c, nil, ErrNoAccessUnits
	}
	coded, err := ProbeCodedFrames(ctx, ffprobePath, c.Path)
	if err != nil {
		return c, nil, err
	}
	if err := VerifyAccessUnitCount(aus, coded); err != nil {
		return c, nil, err
	}
	c.CodedFrames = coded
	c.AccessUnitBoundary = AccessUnitBoundaryLabel(sawAUD)
	if c.NominalSourceFPS > 0 {
		c.MeasuredDurationS = time.Duration(float64(coded) / c.NominalSourceFPS * float64(time.Second)).Seconds()
	}
	if sum, err := fileSHA256(c.Path); err == nil {
		c.SHA256 = sum
	}
	if info, err := os.Stat(c.Path); err == nil {
		c.Bytes = info.Size()
	}
	if res, perr := ProbeClip(ctx, ffprobePath, c.Path); perr == nil {
		c = c.ApplyProbe(res)
		// ApplyProbe would overwrite the verified count with the probe's raw
		// value; both came from the same ffprobe call, but keeping the value
		// the verification actually used avoids any doubt.
		c.CodedFrames = coded
	}
	return c, aus, nil
}

// AccessUnitBoundaryLabel names the rule GroupAccessUnits used, for the
// report, so a reader can tell an exact AUD-derived frame count from the
// single-slice fallback.
func AccessUnitBoundaryLabel(sawAUD bool) string {
	if sawAUD {
		return "aud"
	}
	return "vcl-fallback"
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func commandOutput(ctx context.Context, name string, args ...string) []byte {
	out, _ := commandOutputErr(ctx, name, args...)
	return out
}

func commandOutputErr(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return strings.TrimSpace(string(b[:i]))
	}
	return strings.TrimSpace(string(b))
}
