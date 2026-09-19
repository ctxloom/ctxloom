package claudeengine

// nodeFloorFragment is the prereq an npm-installed engine client
// depends on: a node that can actually PARSE what npm just landed. It lives
// with claude because claude is the only npm-installed engine; lift it when
// a second one appears.
//
// A container-delegation defect lived here. The old prereq was
// `command -v npm || apt-get install -y nodejs npm || true` — "best-effort",
// version-blind. On an Ubuntu 24.04 base that resolves to Node 18.19.1, which
// predates import attributes (`import x from "./p.json" with {type:"json"}`,
// Node 18.20/20.10) — syntax a current npm-published client's entry module
// opens with. So the image built GREEN, the binary sat on PATH, and EVERY
// containerized agent of that engine died at startup with
// `SyntaxError: Unexpected token 'with'` — producing zero ChatEvents, a
// transcript holding only the briefing `user` record at seq 0, and an endless
// coordinator relaunch loop.
//
// So the floor is now asserted, not hoped for: an existing node >= 20 is left
// alone (no network call, no repo added), and only a too-old/absent node
// triggers the vendor's own documented Debian/Ubuntu channel. If the result is
// STILL below the floor, the BUILD fails loudly here rather than shipping an
// image whose agent cannot speak.
//
// SUPPLY CHAIN: deb.nodesource.com is a new download source for these images
// (previously only the distro's own apt repo and the npm registry). It is
// nodejs.org's own documented Debian/Ubuntu install channel, but it is a
// dependency decision — flagged for human review, not slipped in.
const nodeFloorFragment = `RUN set -e \
    && NODE_MAJOR=$( (command -v node >/dev/null 2>&1 && node -p 'process.versions.node.split(".")[0]') || echo 0 ) \
    && if [ "$NODE_MAJOR" -lt 20 ]; then \
         (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates gnupg && rm -rf /var/lib/apt/lists/*)) \
         && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
         && apt-get install -y --no-install-recommends nodejs \
         && rm -rf /var/lib/apt/lists/*; \
       fi \
    && NODE_MAJOR=$(node -p 'process.versions.node.split(".")[0]') \
    && { [ "$NODE_MAJOR" -ge 20 ] || { echo "ctxloom: this base resolves node $(node --version), below the engine clients' floor (>= 20); provide a newer node in the base image" >&2; exit 1; }; }
`

// installFragment installs claude via its OFFICIAL npm package on an
// ARBITRARY base: the asserted node floor (nodeFloorFragment) first, then the
// real install, then a validate gate that RUNS the client (`claude --version`)
// rather than merely locating it.
var installFragment = []byte(nodeFloorFragment + `RUN npm install -g @anthropic-ai/claude-code \
    && claude --version
`)
