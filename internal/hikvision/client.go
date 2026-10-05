// Package hikvision provides a bounded, read-only ISAPI inventory client.
package hikvision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/digestauth"
)

const (
	streamingChannelsPath = "/ISAPI/Streaming/channels"
	maxResponseBytes      = 1 << 20
)

var (
	ErrUnavailable  = errors.New("hikvision: ISAPI endpoint unavailable")
	ErrUnauthorized = errors.New("hikvision: ISAPI authentication rejected")
	ErrMalformed    = errors.New("hikvision: malformed streaming channels response")
)

// Availability is the device-reported configuration state, not connection
// health. Unknown is used when the device does not report an enabled field.
type Availability string

const (
	AvailabilityUnknown  Availability = "unknown"
	AvailabilityEnabled  Availability = "enabled"
	AvailabilityDisabled Availability = "disabled"
)

// Stream is one device-reported Hikvision stream. ID is retained exactly as
// supplied by the recorder and ChannelID is derived only from its documented
// numeric form.
type Stream struct {
	ID           int
	ChannelID    int
	StreamType   int
	Name         string
	Availability Availability
	Codec        string
	Width        int
	Height       int
}

// Channel groups all reported streams of a physical recorder channel.
type Channel struct {
	ID           int
	Name         string
	Availability Availability
	Streams      []Stream
}

// Client has no mutable recorder state and issues GET requests only.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// NewClient creates a client rooted at a recorder HTTP(S) URL. Private-address
// validation belongs to discovery before this client is constructed.
func NewClient(rawBaseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("hikvision: parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("hikvision: unsupported base URL scheme %q", u.Scheme)
	}
	if u.Host == "" || u.User != nil {
		return nil, errors.New("hikvision: base URL must have host and no userinfo")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		baseURL: &url.URL{Scheme: u.Scheme, Host: u.Host},
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// SetTransport replaces the transport for deterministic tests.
func (c *Client) SetTransport(rt http.RoundTripper) { c.http.Transport = rt }

// DiscoverChannels returns the recorder's documented streaming inventory.
// A successful result may contain disabled channels; callers must not equate
// device configuration with current RTSP connectivity.
func (c *Client) DiscoverChannels(ctx context.Context, username, password string) ([]Channel, error) {
	body, err := c.get(ctx, streamingChannelsPath, username, password)
	if err != nil {
		return nil, err
	}
	return parseChannels(body)
}

func (c *Client) get(ctx context.Context, path, username, password string) ([]byte, error) {
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	makeRequest := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/xml")
		return req, nil
	}

	req, err := makeRequest()
	if err != nil {
		return nil, fmt.Errorf("hikvision: create request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hikvision: GET streaming channels: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized && username != "" {
		challenge := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		req, err = makeRequest()
		if err != nil {
			return nil, fmt.Errorf("hikvision: create retry request: %w", err)
		}
		if err := applyAuthorization(req, challenge, username, password); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		resp, err = c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("hikvision: retry GET streaming channels: %w", err)
		}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return readBounded(resp.Body)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrUnauthorized
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return nil, ErrUnavailable
	default:
		return nil, fmt.Errorf("hikvision: streaming channels status %d", resp.StatusCode)
	}
}

func applyAuthorization(req *http.Request, challenge, username, password string) error {
	challenge = strings.TrimSpace(challenge)
	switch {
	case strings.HasPrefix(challenge, "Basic"):
		if req.URL.Scheme != "https" {
			return errors.New("basic authentication requires HTTPS")
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
		return nil
	case strings.HasPrefix(challenge, "Digest"):
		params, err := digestauth.ParseChallenge(challenge)
		if err != nil {
			return err
		}
		header, err := digestauth.BuildAuthorization(username, password, req.Method, req.URL.RequestURI(), params)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", header)
		return nil
	default:
		return errors.New("unsupported WWW-Authenticate challenge")
	}
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("hikvision: read streaming channels: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrMalformed, maxResponseBytes)
	}
	return data, nil
}

type streamingChannelList struct {
	XMLName  xml.Name           `xml:"StreamingChannelList"`
	Channels []streamingChannel `xml:"StreamingChannel"`
}

type streamingChannel struct {
	ID      string `xml:"id"`
	Name    string `xml:"channelName"`
	Enabled *bool  `xml:"enabled"`
	Codec   string `xml:"videoCodecType"`
	Width   int    `xml:"videoResolutionWidth"`
	Height  int    `xml:"videoResolutionHeight"`
}

func parseChannels(data []byte) ([]Channel, error) {
	var list streamingChannelList
	dec := xml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if list.XMLName.Local != "StreamingChannelList" {
		return nil, fmt.Errorf("%w: unexpected root %q", ErrMalformed, list.XMLName.Local)
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if data, ok := tok.(xml.CharData); !ok || strings.TrimSpace(string(data)) != "" {
			return nil, fmt.Errorf("%w: unexpected content after root", ErrMalformed)
		}
	}

	byChannel := make(map[int]*Channel)
	seenStreams := make(map[int]struct{}, len(list.Channels))
	for _, item := range list.Channels {
		streamID, err := strconv.Atoi(strings.TrimSpace(item.ID))
		if err != nil || streamID < 101 {
			return nil, fmt.Errorf("%w: invalid stream id %q", ErrMalformed, item.ID)
		}
		channelID, streamType := streamID/100, streamID%100
		if channelID < 1 || streamType < 1 {
			return nil, fmt.Errorf("%w: invalid stream id %q", ErrMalformed, item.ID)
		}
		if _, duplicate := seenStreams[streamID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate stream id %q", ErrMalformed, item.ID)
		}
		seenStreams[streamID] = struct{}{}
		availability := availabilityFrom(item.Enabled)
		channel := byChannel[channelID]
		if channel == nil {
			channel = &Channel{ID: channelID, Name: strings.TrimSpace(item.Name), Availability: availability}
			byChannel[channelID] = channel
		} else if channel.Name == "" {
			channel.Name = strings.TrimSpace(item.Name)
		}
		channel.Streams = append(channel.Streams, Stream{
			ID: streamID, ChannelID: channelID, StreamType: streamType,
			Name: strings.TrimSpace(item.Name), Availability: availability,
			Codec: strings.TrimSpace(item.Codec), Width: item.Width, Height: item.Height,
		})
		channel.Availability = mergeAvailability(channel.Availability, availability)
	}

	channels := make([]Channel, 0, len(byChannel))
	for _, channel := range byChannel {
		sort.Slice(channel.Streams, func(i, j int) bool { return channel.Streams[i].ID < channel.Streams[j].ID })
		channels = append(channels, *channel)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })
	return channels, nil
}

func availabilityFrom(enabled *bool) Availability {
	if enabled == nil {
		return AvailabilityUnknown
	}
	if *enabled {
		return AvailabilityEnabled
	}
	return AvailabilityDisabled
}

func mergeAvailability(current, next Availability) Availability {
	if current == AvailabilityEnabled || next == AvailabilityEnabled {
		return AvailabilityEnabled
	}
	if current == AvailabilityDisabled && next == AvailabilityDisabled {
		return AvailabilityDisabled
	}
	return AvailabilityUnknown
}

// StreamURI constructs the documented Hikvision RTSP path for a stream that
// was already enumerated by DiscoverChannels. It never includes credentials.
func StreamURI(host string, streamID int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || streamID < 101 {
		return "", errors.New("hikvision: invalid stream URI input")
	}
	return fmt.Sprintf("rtsp://%s/ISAPI/Streaming/channels/%d", host, streamID), nil
}
