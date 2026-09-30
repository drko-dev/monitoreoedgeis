package hikvision

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
)

// Adapter maps the read-only ISAPI streaming inventory to the discovery
// package's existing logical-channel model.
type Adapter struct{ timeout time.Duration }

func NewAdapter(timeout time.Duration) *Adapter { return &Adapter{timeout: timeout} }

func (*Adapter) Vendor() string { return "Hikvision" }

func (a *Adapter) Discover(ctx context.Context, endpoint, username, password string) ([]discovery.VideoSource, error) {
	client, err := NewClient(endpoint, a.timeout)
	if err != nil {
		return nil, errors.New("hikvision: invalid recorder endpoint")
	}
	channels, err := client.DiscoverChannels(ctx, username, password)
	if errors.Is(err, ErrUnauthorized) {
		return nil, discovery.ErrRecorderAuthRequired
	}
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("hikvision: invalid recorder endpoint")
	}

	out := make([]discovery.VideoSource, 0, len(channels))
	for _, channel := range channels {
		source := discovery.VideoSource{
			SourceToken:   fmt.Sprintf("hikvision:channel:%d", channel.ID),
			ChannelNumber: channel.ID,
			Label:         channel.Name,
			Availability:  mapAvailability(channel.Availability),
		}
		for _, stream := range channel.Streams {
			uri, err := StreamURI(u.Hostname(), stream.ID)
			if err != nil {
				return nil, err
			}
			role := discovery.StreamRoleUnknown
			switch stream.StreamType {
			case 1:
				role = discovery.StreamRoleMainStream
			case 2:
				role = discovery.StreamRoleSubStream
			}
			source.Profiles = append(source.Profiles, discovery.MediaProfile{
				Token:           fmt.Sprintf("hikvision:stream:%d", stream.ID),
				Name:            strings.TrimSpace(stream.Name),
				Codec:           stream.Codec,
				Width:           stream.Width,
				Height:          stream.Height,
				StreamURI:       uri,
				StreamURIOrigin: "hikvision_constructed",
				Availability:    mapAvailability(stream.Availability),
				Role:            role,
				ChannelNumber:   stream.ChannelID,
				StreamID:        stream.ID,
			})
		}
		out = append(out, source)
	}
	return out, nil
}

func mapAvailability(value Availability) discovery.ChannelAvailability {
	switch value {
	case AvailabilityEnabled:
		return discovery.ChannelAvailabilityEnabled
	case AvailabilityDisabled:
		return discovery.ChannelAvailabilityDisabled
	default:
		return discovery.ChannelAvailabilityUnknown
	}
}
