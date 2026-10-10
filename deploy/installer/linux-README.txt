GEO CAM Edge Installer (Linux)

Run:
  ./geocam-edge-ui

Requirements: a desktop session and GTK 3 + WebKitGTK 4.1, e.g. on
Ubuntu 24.04+ / Debian 13+:
  sudo apt-get install libgtk-3-0t64 libwebkit2gtk-4.1-0

This build is not code-signed. Verify the download against SHA256SUMS.txt
from the same GitHub Release:
  sha256sum -c --ignore-missing SHA256SUMS.txt

This is the desktop installer GUI. The Linux appliance/daemon is published
separately under the vX.Y.Z tags (see docs/RELEASING.md).
