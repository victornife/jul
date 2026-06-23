import { z } from "zod";

const OverviewSchema = z.object({
  product: z.string(),
  version: z.string(),
  status: z.string(),
});

export type Overview = z.infer<typeof OverviewSchema>;

async function api<T>(path: string): Promise<T> {
  const token = sessionStorage.getItem("jul_admin_token") ?? "";
  const resp = await fetch(`/api${path}`, {
    headers: {
      Accept: "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
  });
  if (!resp.ok) throw new Error(`API ${path}: ${resp.status} ${resp.statusText}`);
  return resp.json() as Promise<T>;
}

export function fetchOverview(): Promise<Overview> {
  return api<unknown>("/runtime/overview")
    .then((data) => OverviewSchema.parse(data));
}
