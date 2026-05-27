import { Navigate } from 'react-router-dom';

/**
 * Silent token renewal (oidc-client-ts iframe flow) is no longer used.
 * Token refresh is handled server-side by dc-api. Redirect to /login
 * in the unlikely event a stale URL still points here.
 */
export default function SilentCallbackPage() {
  return <Navigate to="/login" replace />;
}
