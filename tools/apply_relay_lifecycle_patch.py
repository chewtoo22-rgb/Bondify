from pathlib import Path

path = Path("core/bond/relay.go")
src = path.read_text()

replacements = [
    (
        "\tbyClientKey      map[[crypto.KeyLen]byte]*relaySession\n\thandshakeLimiter *handshakeLimiter\n\tnewResponder     func(crypto.Keypair) (*crypto.Responder, error)\n",
        "\tbyClientKey          map[[crypto.KeyLen]byte]*relaySession\n\thandshakeLimiter     *handshakeLimiter\n\tclientHandshakeLocks handshakeClientLocks\n\tnewResponder          func(crypto.Keypair) (*crypto.Responder, error)\n",
        "Relay handshake lock field",
    ),
    (
        """\tr.mu.Lock()
\texisting := r.byClientKey[clientKey]
\tr.mu.Unlock()

\tvar tunnelIP net.IP
\tvar sessionIndex uint32
\tallocated := false
\tif existing != nil {
\t\ttunnelIP = existing.tunnelIP
\t\tsessionIndex = existing.sessionIndex
\t} else {
\t\tip, err := r.pool.Allocate()
\t\tif err != nil {
\t\t\tlog.Printf(\"bond: relay ip pool exhausted: %v\", err)
\t\t\treturn
\t\t}
\t\ttunnelIP = ip
\t\tsessionIndex = r.newSessionIndex()
\t\tallocated = true
\t}
""",
        """\t// Serialize the authenticated lifecycle for this client identity. Different clients
\t// still proceed independently through the bounded stripe table, while duplicate first
\t// handshakes cannot race allocation/publication and strand an IP or session.
\tunlockClient := r.clientHandshakeLocks.lock(clientKey)
\tdefer unlockClient()

\tr.mu.RLock()
\texisting := r.byClientKey[clientKey]
\tr.mu.RUnlock()

\tvar tunnelIP net.IP
\tvar sessionIndex uint32
\tvar lease *handshakeLease
\tif existing != nil {
\t\ttunnelIP = existing.tunnelIP
\t\tsessionIndex = existing.sessionIndex
\t} else {
\t\tlease, err = allocateHandshakeLease(r.pool)
\t\tif err != nil {
\t\t\tlog.Printf(\"bond: relay ip pool exhausted: %v\", err)
\t\t\treturn
\t\t}
\t\tdefer lease.Release()
\t\ttunnelIP = lease.IP()
\t\tsessionIndex = r.newSessionIndex()
\t}
""",
        "serialized lookup and leased allocation",
    ),
    (
        """\trs, err := newRelaySession(r, sessionIndex, sess, tunnelIP, r.cfg)
\tif err != nil {
\t\tlog.Printf(\"bond: create relay session: %v\", err)
\t\tif allocated {
\t\t\tr.pool.Release(tunnelIP)
\t\t}
\t\treturn
\t}
""",
        """\trs, err := newRelaySession(r, sessionIndex, sess, tunnelIP, r.cfg)
\tif err != nil {
\t\tlog.Printf(\"bond: create relay session: %v\", err)
\t\treturn
\t}
""",
        "deferred lease cleanup",
    ),
    (
        """\tr.mu.Lock()
\tr.byIndex[sessionIndex] = rs
\tr.byTunnelIP[tunnelIP.String()] = rs
\tr.byClientKey[clientKey] = rs
\tr.mu.Unlock()
""",
        """\tr.mu.Lock()
\tr.byIndex[sessionIndex] = rs
\tr.byTunnelIP[tunnelIP.String()] = rs
\tr.byClientKey[clientKey] = rs
\tif lease != nil {
\t\t// All live lookup maps now agree on the new session. Transfer the fresh address
\t\t// from temporary handshake ownership to the published session before unlocking.
\t\tlease.Publish()
\t}
\tr.mu.Unlock()
""",
        "atomic lease publication",
    ),
]

for old, new, label in replacements:
    count = src.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected exactly one match, got {count}")
    src = src.replace(old, new, 1)

path.write_text(src)
