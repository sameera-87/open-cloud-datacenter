import { Navigate } from 'react-router-dom';

/**
 * This route is no longer used. The Asgardeo callback is now handled
 * entirely by dc-api at GET /v1/auth/callback — the browser never lands
 * on a cloud-ui /callback page in the BFF cookie flow.
 *
 * The route entry is kept in the router for a graceful redirect in case
 * a stale bookmark or old redirect_uri configuration sends the browser
 * here. Users are bounced to /login where they can start a fresh sign-in.
 */
export default function CallbackPage() {
  return <Navigate to="/login" replace />;
}
