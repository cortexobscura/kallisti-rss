# Licensing and dependencies

This new implementation uses the Unlicense (LICENSE), matching the two referenced public projects as the owner-confirmed first-party license. No upstream Go source or generated Mumble bindings were copied; wire constants and protocol behavior were independently implemented from the public protocol. The Unlicense text itself is reused verbatim.

Original license and notice texts for the 15 compiled modules are copied under licenses/ (27 files); licenses/README.md records exact versions. Go dependencies remain separate modules, fetched through go.mod/go.sum; no vendored dependency source or binaries are in this bundle. Review their actual module LICENSE files before distributing compiled binaries. Direct dependencies:

- github.com/coder/websocket — ISC
- golang.org/x/image, x/net, x/sys — BSD-3-Clause
- google.golang.org/protobuf — BSD-3-Clause
- gopkg.in/yaml.v3 — MIT and Apache-2.0 components
- modernc.org/sqlite — BSD-3-Clause wrapper and SQLite public-domain upstream, with transitive components

`go list -m all` gives the complete exact dependency graph. Transitive licenses remain applicable; this summary is not a replacement for notices. Source test RSS/Atom, generated colored pixels, and demo credentials are original synthetic fixtures, not copies of articles/photos from Reddit or Aniyuki. Public feed availability does not confer rehosting rights; operators must choose appropriate sources.

The final container includes this document, the first-party LICENSE, all bundled dependency notices and the build toolchain Go LICENSE under /usr/share/doc/kallisti-rss/. Include the same notices alongside any separately distributed binary. The first-party Unlicense does not replace third-party license obligations.
