# Upstream dependency notices

`nicecloud-kernel` directly links `github.com/sagernet/sing-box v1.12.12`.
Protocol processing, REALITY, Vision, AnyTLS, TLS ECH, routing and V2Ray statistics
are implemented by that dependency and its transitive upstream libraries.

The adapter code in this directory is GPL-3.0-or-later. sing-box is
GPL-3.0-or-later with the additional naming condition reproduced in
`UPSTREAM-LICENSE`; this binary is named **nicecloud-kernel** and is not an
official sing-box product. The GPL text is in `COPYING`.

Source for this adapter is the complete `proxy-kernel/` directory. Dependency
versions and checksums are recorded in `go.mod` and `go.sum`; upstream source is
available at <https://github.com/SagerNet/sing-box/tree/v1.12.12> and via Go module
download. Anyone distributing a linked binary should supply the corresponding
source and notices in accordance with these licenses. Retain this directory's
source and dependency metadata with releases; changing the application name
does not change the upstream license obligations.
