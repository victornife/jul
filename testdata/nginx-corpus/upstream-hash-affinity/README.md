# Upstream hash affinity migration fixture

Repository-authored AGPL-3.0-only fixture for #432. It covers NGINX `ip_hash` (with a weighted member and a `down` member), `hash $cookie_session_id consistent` on an HTTP upstream, and `hash $remote_addr consistent` on a stream upstream. Each translates onto Jul's `consistent_hash` with the `client_ip` or `cookie` key source and is classified `approximated`: the key source matches, the placement function does not, so keys are re-placed once at cutover.
