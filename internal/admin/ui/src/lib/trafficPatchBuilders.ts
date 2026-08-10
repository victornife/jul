/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  ConfigPatch,
  GlobalSettingsProjection,
  ServerLimitsProjection,
  TrafficControls,
} from "@/api/client.ts";

export interface GlobalSettingsDraft {
  workerThreads: string;
  logLevel: "debug" | "info" | "warn" | "error";
  logFormat: "text" | "json";
  shutdownTimeout: string;
  reloadTimeout: string;
  redactMinSecretLength: number;
}

export interface CompressionDraft {
  enabled: boolean;
  encoders: string[];
  level: number;
  minSize: string;
  types: string[];
  precompressed: boolean;
}

export interface CacheDraft {
  enabled: boolean;
  memoryMaxSize: string;
  diskPath: string;
  diskMaxSize: string;
  defaultTTL: string;
  staleWhileRevalidate: string;
  staleIfError: string;
}

export interface RateLimitDraft {
  enabled: boolean;
  key: string;
  rate: number;
  burst: number;
  maxConns: number;
}

export interface ServerLimitsDraft {
  bodyLimit: string;
  readTimeout: string;
  writeTimeout: string;
  idleTimeout: string;
}

function sameArray(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function hasOwnFields(value: object): boolean {
  return Object.keys(value).length > 0;
}

export function seedGlobalSettings(current: GlobalSettingsProjection): GlobalSettingsDraft {
  return {
    workerThreads: current.worker_threads,
    logLevel: current.log_level,
    logFormat: current.log_format,
    shutdownTimeout: current.shutdown_timeout,
    reloadTimeout: current.reload_timeout,
    redactMinSecretLength: current.redact_min_secret_length,
  };
}

export function buildGlobalPatch(
  initial: GlobalSettingsDraft,
  current: GlobalSettingsDraft,
): ConfigPatch | null {
  const global: Extract<ConfigPatch, { op: "global_set" }>["global"] = {};
  if (current.workerThreads !== initial.workerThreads) global.worker_threads = current.workerThreads;
  if (current.logLevel !== initial.logLevel) global.log_level = current.logLevel;
  if (current.logFormat !== initial.logFormat) global.log_format = current.logFormat;
  if (current.shutdownTimeout !== initial.shutdownTimeout) {
    global.shutdown_timeout = current.shutdownTimeout;
  }
  if (current.reloadTimeout !== initial.reloadTimeout) global.reload_timeout = current.reloadTimeout;
  if (current.redactMinSecretLength !== initial.redactMinSecretLength) {
    global.redact_min_secret_length = current.redactMinSecretLength;
  }
  return hasOwnFields(global) ? { op: "global_set", global } : null;
}

export function seedCompression(current: TrafficControls): CompressionDraft {
  const compression = current.compression;
  return {
    enabled: compression?.enabled ?? false,
    encoders: [...(compression?.encoders ?? [])],
    level: compression?.level ?? 0,
    minSize: compression?.min_size ?? "",
    types: [...(compression?.types ?? [])],
    precompressed: compression?.precompressed ?? false,
  };
}

export function buildCompressionPatch(
  initial: CompressionDraft,
  current: CompressionDraft,
): ConfigPatch | null {
  const compression: Extract<ConfigPatch, { op: "compression_set" }>["compression"] = {};
  if (current.enabled !== initial.enabled) compression.enabled = current.enabled;
  if (!sameArray(current.encoders, initial.encoders)) compression.encoders = [...current.encoders];
  if (current.level !== initial.level) compression.level = current.level;
  if (current.minSize !== initial.minSize) compression.min_size = current.minSize;
  if (!sameArray(current.types, initial.types)) compression.types = [...current.types];
  if (current.precompressed !== initial.precompressed) {
    compression.precompressed = current.precompressed;
  }
  return hasOwnFields(compression) ? { op: "compression_set", compression } : null;
}

export function seedRateLimit(current: TrafficControls): RateLimitDraft {
  const rateLimit = current.rate_limit;
  return {
    enabled: rateLimit?.enabled ?? false,
    key: rateLimit?.key ?? "",
    rate: rateLimit?.rate ?? 0,
    burst: rateLimit?.burst ?? 0,
    maxConns: rateLimit?.max_conns ?? 0,
  };
}

export function buildGlobalRateLimitPatch(
  initial: RateLimitDraft,
  current: RateLimitDraft,
): ConfigPatch | null {
  const rate_limit: Extract<ConfigPatch, { op: "rate_limit_global_set" }>["rate_limit"] = {};
  if (current.enabled !== initial.enabled) rate_limit.enabled = current.enabled;
  if (current.key !== initial.key) rate_limit.key = current.key;
  if (current.rate !== initial.rate) rate_limit.rate = current.rate;
  if (current.burst !== initial.burst) rate_limit.burst = current.burst;
  if (current.maxConns !== initial.maxConns) rate_limit.max_conns = current.maxConns;
  return hasOwnFields(rate_limit) ? { op: "rate_limit_global_set", rate_limit } : null;
}

export function seedCache(current: TrafficControls): CacheDraft {
  const cache = current.cache;
  return {
    enabled: cache?.enabled ?? false,
    memoryMaxSize: cache?.memory_max_size ?? cache?.memory_max ?? "",
    diskPath: cache?.disk_path ?? "",
    diskMaxSize: cache?.disk_max_size ?? "",
    defaultTTL: cache?.default_ttl ?? "",
    staleWhileRevalidate: cache?.stale_while_revalidate ?? "",
    staleIfError: cache?.stale_if_error ?? "",
  };
}

export function seedServerLimits(route: ServerLimitsProjection): ServerLimitsDraft {
  return {
    bodyLimit: route.client_max_body_size,
    readTimeout: route.read_timeout,
    writeTimeout: route.write_timeout,
    idleTimeout: route.idle_timeout,
  };
}

export function buildServerLimitsPatch(
  listen: string,
  initial: ServerLimitsDraft,
  current: ServerLimitsDraft,
): ConfigPatch | null {
  const limits: Extract<ConfigPatch, { op: "server_set_limits" }>["limits"] = {};
  if (current.bodyLimit !== initial.bodyLimit) limits.client_max_body_size = current.bodyLimit;
  if (current.readTimeout !== initial.readTimeout) limits.read_timeout = current.readTimeout;
  if (current.writeTimeout !== initial.writeTimeout) limits.write_timeout = current.writeTimeout;
  if (current.idleTimeout !== initial.idleTimeout) limits.idle_timeout = current.idleTimeout;
  return hasOwnFields(limits) ? { op: "server_set_limits", listen, limits } : null;
}
