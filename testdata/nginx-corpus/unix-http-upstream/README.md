# Unix HTTP upstream migration fixture

Repository-authored AGPL-3.0-only fixture for #407/#365. It exercises the common NGINX shape where an ordinary HTTP `proxy_pass` targets a named upstream whose member is a Unix-domain socket. The selected runtime scenario asserts status/body only, avoiding a false equivalence claim for NGINX and Jul Host defaults.
