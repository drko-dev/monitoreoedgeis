# Third-party dependency notices — DRAFT / NOT FOR RELEASE

These license labels and URLs originate solely from the checked-in lock file.
They are **not verified against actual wheel archives**. Actual LICENSE,
METADATA, copyrights and transitive native libraries must be audited.
No redistribution is authorized by this file.

| Name | Version | License declared | Origin declared | Verification |
|---|---|---|---|---|
| certifi | 2026.7.22 | MPL-2.0 | https://pypi.org/project/certifi/2026.7.22/ | UNVERIFIED |
| charset-normalizer | 3.5.2 | MIT | https://pypi.org/project/charset-normalizer/3.5.2/ | UNVERIFIED |
| contourpy | 1.3.3 | BSD-3-Clause | https://pypi.org/project/contourpy/1.3.3/ | UNVERIFIED |
| cycler | 0.12.1 | BSD-3-Clause | https://pypi.org/project/cycler/0.12.1/ | UNVERIFIED |
| filelock | 4.1.1 | MIT | https://pypi.org/project/filelock/4.1.1/ | UNVERIFIED |
| fonttools | 4.66.1 | MIT | https://pypi.org/project/fonttools/4.66.1/ | UNVERIFIED |
| fsspec | 2026.9.0 | BSD-3-Clause | https://pypi.org/project/fsspec/2026.9.0/ | UNVERIFIED |
| idna | 3.20 | BSD-3-Clause | https://pypi.org/project/idna/3.20/ | UNVERIFIED |
| jinja2 | 3.1.6 | BSD-3-Clause | https://pypi.org/project/jinja2/3.1.6/ | UNVERIFIED |
| kiwisolver | 1.5.1 | BSD-3-Clause | https://pypi.org/project/kiwisolver/1.5.1/ | UNVERIFIED |
| markupsafe | 3.0.4 | Permissive | https://pypi.org/project/markupsafe/3.0.4/ | UNVERIFIED |
| matplotlib | 3.11.2 | PSF-compatible | https://pypi.org/project/matplotlib/3.11.2/ | UNVERIFIED |
| mpmath | 1.3.0 | BSD-3-Clause | https://pypi.org/project/mpmath/1.3.0/ | UNVERIFIED |
| networkx | 3.6.1 | BSD-3-Clause | https://pypi.org/project/networkx/3.6.1/ | UNVERIFIED |
| numpy | 1.26.4 | BSD-3-Clause | https://pypi.org/project/numpy/1.26.4/ | UNVERIFIED |
| opencv-python | 4.11.0.86 | Apache-2.0 | https://pypi.org/project/opencv-python/4.11.0.86/ | UNVERIFIED |
| packaging | 26.3 | Apache-2.0 OR BSD-2-Clause | https://pypi.org/project/packaging/26.3/ | UNVERIFIED |
| pandas | 3.0.6 | BSD-3-Clause | https://pypi.org/project/pandas/3.0.6/ | UNVERIFIED |
| pillow | 10.4.0 | MIT-CMU | https://pypi.org/project/pillow/10.4.0/ | UNVERIFIED |
| psutil | 7.2.2 | BSD-3-Clause | https://pypi.org/project/psutil/7.2.2/ | UNVERIFIED |
| py-cpuinfo | 9.0.0 | MIT | https://pypi.org/project/py-cpuinfo/9.0.0/ | UNVERIFIED |
| pyparsing | 3.3.3 | MIT | https://pypi.org/project/pyparsing/3.3.3/ | UNVERIFIED |
| python-dateutil | 2.9.0.post0 | Apache-2.0 OR BSD-3-Clause | https://pypi.org/project/python-dateutil/2.9.0.post0/ | UNVERIFIED |
| pyyaml | 6.0.3 | MIT | https://pypi.org/project/pyyaml/6.0.3/ | UNVERIFIED |
| requests | 2.34.2 | Apache-2.0 | https://pypi.org/project/requests/2.34.2/ | UNVERIFIED |
| scipy | 1.17.1 | BSD-3-Clause | https://pypi.org/project/scipy/1.17.1/ | UNVERIFIED |
| seaborn | 0.13.2 | BSD-3-Clause | https://pypi.org/project/seaborn/0.13.2/ | UNVERIFIED |
| six | 1.17.0 | MIT | https://pypi.org/project/six/1.17.0/ | UNVERIFIED |
| sympy | 1.14.0 | BSD-3-Clause | https://pypi.org/project/sympy/1.14.0/ | UNVERIFIED |
| torch | 2.4.0 | BSD-3-Clause | https://pypi.org/project/torch/2.4.0/ | UNVERIFIED |
| torchvision | 0.19.0 | BSD-3-Clause | https://pypi.org/project/torchvision/0.19.0/ | UNVERIFIED |
| tqdm | 4.70.1 | MPL-2.0 AND MIT | https://pypi.org/project/tqdm/4.70.1/ | UNVERIFIED |
| typing-extensions | 4.16.0 | PSF-2.0 | https://pypi.org/project/typing-extensions/4.16.0/ | UNVERIFIED |
| ultralytics | 8.3.1 | AGPL-3.0 | https://pypi.org/project/ultralytics/8.3.1/ | UNVERIFIED |
| ultralytics-thop | 2.2.2 | AGPL-3.0 | https://pypi.org/project/ultralytics-thop/2.2.2/ | UNVERIFIED |
| urllib3 | 2.8.0 | MIT | https://pypi.org/project/urllib3/2.8.0/ | UNVERIFIED |

## Additional runtime components requiring source and legal verification

- CPython and python-build-standalone: original archive licenses, notices and bundled shared libraries.
- FFmpeg and FFprobe: actual build configure flags, LGPL/GPL source offer, patches, linked libraries; libx264 is requested by current clip encoding.
- Vision Worker: its code, imports, model framework and packaged license texts.
- Model weights: not licensed for redistribution in this release.

## Required gates before release

1. Independently verify each artifact hash, source and legal notices.
2. Choose and document a valid Ultralytics AGPL-compliance or Enterprise contract path.
3. Choose FFmpeg GPL-compliant distribution or a validated LGPL-compatible codec/build path.
4. Have the product owner and legal reviewer approve the exact release artifacts in an authenticated, separately managed process.
5. Create a real sealed Runtime Pack and release pin, then pass functional offline tests.
