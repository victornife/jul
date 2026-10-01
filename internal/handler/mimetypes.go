// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"mime"
	"strings"
)

// streamingMediaTypes are the Content-Types static serving uses for streaming
// media that Go's built-in table does not know (#510). Go otherwise relies on a
// system MIME database that the distroless image and minimal hosts lack, so
// HLS playlists were sniffed as text/plain and segments as octet-stream.
//
// This table takes precedence over the system database for these extensions
// only, so a container and a host serve the same bytes with the same type: the
// Debian/Ubuntu media-types package maps .ts to text/vnd.trolltech.linguist
// (Qt translation sources), which strict HLS players reject. Every value is an
// IANA-registered media type.
var streamingMediaTypes = map[string]string{
	".aac":  "audio/aac",                     // IANA audio/aac
	".m3u8": "application/vnd.apple.mpegurl", // RFC 8216 §4
	".m4s":  "video/iso.segment",             // IANA video/iso.segment
	".mkv":  "video/matroska",                // RFC 9559 §27.1
	".mpd":  "application/dash+xml",          // ISO/IEC 23009-1 Annex C
	".ts":   "video/mp2t",                    // RFC 3555 §4.2.9
}

// systemTypeByExtension is mime.TypeByExtension, replaceable in tests to model
// a host with or without a MIME database.
var systemTypeByExtension = mime.TypeByExtension

// contentTypeByExtension returns the Content-Type static serving assigns to a
// file extension: the streaming-media table first, then Go's built-in and
// system tables. It returns "" when neither knows the extension, leaving the
// caller to sniff or fall back.
func contentTypeByExtension(ext string) string {
	if t, ok := streamingMediaTypes[strings.ToLower(ext)]; ok {
		return t
	}
	return systemTypeByExtension(ext)
}
