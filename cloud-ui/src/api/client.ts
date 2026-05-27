import createClient, { type Middleware } from 'openapi-fetch';
import type { paths } from './generated/types';

// Empty default → relative paths → caught by the Vite dev proxy in
// vite.config.ts (which forwards /v1/* to the deployed dc-api).
// In production, set VITE_API_BASE to the full dc-api host
// (e.g. https://dcapi.lk.internal.wso2.com) so calls go cross-origin.
const DEFAULT_BASE_URL = '';

const baseUrl =
  (import.meta.env.VITE_API_BASE as string | undefined)?.replace(/\/$/, '') ?? DEFAULT_BASE_URL;

/**
 * Typed DC-API client.
 *
 * Authentication uses the dc-api BFF cookie flow: every request carries
 * `credentials: 'include'` so the HttpOnly `dcapi_session` cookie
 * travels to dc-api, where the auth middleware extracts and validates
 * the sealed access token.
 */
export function makeApiClient() {
  const client = createClient<paths>({ baseUrl });

  const credentialsMiddleware: Middleware = {
    async onRequest({ request }) {
      // Cookie-based auth: the browser sends the dcapi_session cookie
      // automatically when credentials:include is set on the fetch call.
      // openapi-fetch doesn't expose a direct `credentials` option on the
      // client constructor, so we clone the request with the flag set.
      // The clone is required because Request objects are immutable.
      return new Request(request, { credentials: 'include' });
    },
  };
  client.use(credentialsMiddleware);

  return client;
}

export type ApiClient = ReturnType<typeof makeApiClient>;
