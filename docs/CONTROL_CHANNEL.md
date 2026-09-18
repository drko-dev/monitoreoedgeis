# L8-L10 Outbound Control Channel

The Edge initiates every control-plane exchange with an authenticated HTTPS poll
to SaaS. SaaS never opens a connection to an Edge and Edge adds no inbound
listener. Therefore NAT, port forwarding, public IPs, and SaaS-to-site
reachability are not dependencies.

The channel reuses transport retry/credential handling and the discovery module's
serialized scan lifecycle. L6 frame and event buffers are not part of this
channel and remain unchanged. Commands are UUID-addressed and allowlisted:
`request_status` reads local state and `rediscovery` calls the existing scan.
`reload_config` and `restart_video_pipeline` are reported as unsupported because
there is no safe live lifecycle primitive. Payloads are empty and no command is
sent to a shell.

Site-to-site VPN, Tailscale subnet routing, or a WireGuard gateway may carry the
same outbound HTTPS traffic, but none is mandatory. This mechanism does not make
claims about physical outages.
