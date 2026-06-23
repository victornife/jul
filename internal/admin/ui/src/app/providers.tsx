import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchInterval: 15_000,
      retry: 1,
    },
  },
});

export function Providers({ children }: { readonly children: React.ReactNode }) {
  // Derive the router basename from the Vite base URL so client-side routing
  // works wherever the SPA is mounted (e.g. /console/v2/). Trailing slashes are
  // trimmed because react-router expects a bare basename.
  const basename = import.meta.env.BASE_URL.replace(/\/$/, "");
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={basename}>{children}</BrowserRouter>
    </QueryClientProvider>
  );
}
