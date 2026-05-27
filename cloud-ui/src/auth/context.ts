import { createContext } from 'react';

/**
 * User identity returned by GET /v1/auth/me and cached in AuthProvider.
 * The browser never sees the access token — it lives in the HttpOnly
 * dcapi_session cookie held by dc-api.
 *
 * Fields map 1-to-1 to the AuthMe schema in openapi.yaml. `is_admin`
 * and `tenants` drive the no-tenants welcome screen introduced in
 * Option D (dc-api owns tenancy; IdP is authn-only).
 */
export interface AuthUser {
  sub: string;
  email?: string;
  /** ISO-8601 timestamp when the session cookie expires. */
  expiresAt: string;
  /**
   * True when the signed-in user is a platform admin (sourced from
   * DCAPI_PLATFORM_ADMIN_SUBS or the dc-admin Asgardeo group).
   * Admins bypass per-tenant RBAC and can see all tenants.
   */
  isAdmin: boolean;
  /**
   * Tenant IDs from the ID-token groups claim (stripped prefix).
   * For non-admins this is the authoritative "what can you access" list.
   * Empty array = user is authenticated but has no tenant invitations yet.
   */
  tenants: string[];
}

export interface AuthContextValue {
  /** Current user, or null when signed out. */
  user: AuthUser | null;
  /** True while the initial GET /v1/auth/me call is in flight. */
  loading: boolean;
  /**
   * Initiates sign-in by navigating to GET /v1/auth/login.
   * Pass an absolute `returnTo` URL to deep-link back after auth.
   */
  login: (returnTo?: string) => void;
  /** Clears the local user state and navigates to POST /v1/auth/logout. */
  logout: () => Promise<void>;
}

export const AuthContext = createContext<AuthContextValue | null>(null);
