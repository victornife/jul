# Common idioms runtime

Repository-authored, synthetic AGPL-3.0-only fixture for #523. No production data.

The exact candidate golden covers expiration, MIME, body limits, gzip types,
WebSocket recognition and proxy flush policy. Real Jul and the pinned NGINX lane
assert Cache-Control for positive, negative, zero, off and ineligible responses.
Absolute Expires values depend on wall time; separate middleware and paired
runtime tests verify their offset. MIME file bodies, original-extension sidecars,
HEAD/304/ranges, and cache storage isolation have dedicated runtime tests.

The WebSocket endpoint is assessment-only here; the existing websocket-runtime
fixture executes native upgrades. No arbitrary proxy header or retry policy is
claimed equivalent.
