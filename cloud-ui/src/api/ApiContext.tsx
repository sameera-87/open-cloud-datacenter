import { useMemo, type ReactNode } from 'react';
import { ApiContext } from './context';
import { makeApiClient } from './client';

export function ApiProvider({ children }: { children: ReactNode }) {
  // The client is stable for the lifetime of the app — credentials are
  // managed via the HttpOnly dcapi_session cookie, not passed per-render.
  const client = useMemo(() => makeApiClient(), []);
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>;
}
