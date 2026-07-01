// Client-side generator for the gRPC route designer (Phase 2). It emits a
// complete [[servers]] block with a grpc_transcode location, then appends it
// to the running configuration and routes through the authoritative
// Validate → Diff → Apply → Rollback pipeline.

export interface TranscodeRouteDraft {
	listen: string;
	serverNames: string;
	path: string;
	matchType: "prefix" | "exact" | "regex";
	target: string; // upstream name or host:port
	descriptorSet: string; // path on disk (where the backend saved the .pb)
	tls: boolean;
	preserveNames: boolean;
	streaming: boolean;
	streamMode: "ndjson" | "sse";
}

function tomlString(s: string): string {
	return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
	return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

/**
 * Generates a complete [[servers]] block for a gRPC transcoding route.
 *
 * The descriptor_set path is the absolute/relative path on the server's
 * filesystem where the uploaded .pb was saved (e.g. via plugin_upload_dir or
 * configured separately). The designer seeds it from the file path the operator
 * enters; Jul does not manage descriptor file placement automatically.
 */
export function generateTranscodeRouteToml(d: TranscodeRouteDraft): string {
	const lines: string[] = [];
	lines.push("[[servers]]");
	lines.push(`listen = ${tomlString(d.listen.trim() || ":8080")}`);
	const names = d.serverNames
		.split(",")
		.map((s) => s.trim())
		.filter((s) => s.length > 0);
	if (names.length > 0) {
		lines.push(`server_names = ${tomlStringArray(names)}`);
	}
	lines.push("");
	lines.push("  [[servers.locations]]");
	lines.push(
		`  match = { type = ${tomlString(d.matchType)}, path = ${tomlString(d.path.trim() || "/")} }`,
	);
	lines.push("    [servers.locations.grpc_transcode]");
	lines.push(`    target = ${tomlString(d.target.trim())}`);
	lines.push(`    descriptor_set = ${tomlString(d.descriptorSet.trim())}`);
	if (d.tls) lines.push(`    tls = true`);
	if (d.preserveNames) lines.push(`    preserve_proto_field_names = true`);
	if (d.streaming) {
		lines.push(`    streaming = true`);
		lines.push(`    stream_mode = ${tomlString(d.streamMode)}`);
	}
	return lines.join("\n");
}

/**
 * Quick validation for a transcode draft. Returns empty array when valid.
 */
export function transcodeDraftWarnings(d: TranscodeRouteDraft): string[] {
	const w: string[] = [];
	if (d.listen.trim() === "") w.push("Listen address is required.");
	if (d.path.trim() === "") w.push("Match path is required.");
	if (d.target.trim() === "") w.push("gRPC backend target is required.");
	if (d.descriptorSet.trim() === "") w.push("Descriptor set path is required.");
	return w;
}
