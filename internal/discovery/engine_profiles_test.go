package discovery

import "testing"

func TestAssociateProfilesByVideoSourceToken(t *testing.T) {
	sources := []VideoSource{
		{SourceToken: "ch1", Availability: ChannelAvailabilityUnknown},
		{SourceToken: "ch4", Availability: ChannelAvailabilityUnknown},
	}
	profiles := []MediaProfile{
		{Token: "p4", VideoSourceToken: "ch4"},
		{Token: "p1", VideoSourceToken: "ch1"},
		{Token: "orphan", VideoSourceToken: "ch99"},
		{Token: "untagged"},
	}

	got := associateProfiles(sources, profiles)
	if len(got[0].Profiles) != 1 || got[0].Profiles[0].Token != "p1" {
		t.Fatalf("ch1 profiles = %+v, want only p1", got[0].Profiles)
	}
	if len(got[1].Profiles) != 1 || got[1].Profiles[0].Token != "p4" {
		t.Fatalf("ch4 profiles = %+v, want only p4", got[1].Profiles)
	}
}

func TestAssociateProfilesSingleSourceAllowsUntaggedProfile(t *testing.T) {
	got := associateProfiles(
		[]VideoSource{{SourceToken: "only"}},
		[]MediaProfile{{Token: "legacy"}},
	)
	if len(got[0].Profiles) != 1 || got[0].Profiles[0].Token != "legacy" {
		t.Fatalf("profiles = %+v, want legacy profile", got[0].Profiles)
	}
}

func TestAssociateProfilesWithoutSourcesKeepsDistinctTaggedSources(t *testing.T) {
	got := associateProfiles(nil, []MediaProfile{
		{Token: "p8", VideoSourceToken: "ch8"},
		{Token: "p2", VideoSourceToken: "ch2"},
	})
	if len(got) != 2 || got[0].SourceToken != "ch2" || got[1].SourceToken != "ch8" {
		t.Fatalf("sources = %+v, want sorted ch2/ch8", got)
	}
}

func TestDeduplicateDevicesPrefersDiscoveredIdentityOverMatchingManualSeed(t *testing.T) {
	xaddr := "http://192.168.1.20/ISAPI"
	got := deduplicateDevices([]DiscoveredDevice{
		{StableIdentity: "epr:uuid-recorder", XAddr: xaddr, AllXAddrs: []string{xaddr}},
		{StableIdentity: "manual:hikvision:192.168.1.20:80", XAddr: xaddr, AllXAddrs: []string{xaddr}},
	})
	if len(got) != 1 || got[0].StableIdentity != "epr:uuid-recorder" {
		t.Fatalf("devices = %+v, want one WS-Discovery identity", got)
	}
}
