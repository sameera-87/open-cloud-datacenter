/**
 * DEPRECATED — no longer used.
 *
 * Browser-side OIDC via oidc-client-ts was replaced by the dc-api BFF
 * cookie flow (GET /v1/auth/login → Asgardeo → GET /v1/auth/callback →
 * HttpOnly dcapi_session cookie). See src/auth/AuthContext.tsx.
 *
 * This file is kept as a tombstone so git history stays readable.
 * Nothing in the codebase imports from it; it can be deleted once the
 * BFF is live in every region.
 */
