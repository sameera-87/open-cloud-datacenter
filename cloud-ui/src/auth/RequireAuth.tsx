import { Spinner, makeStyles, tokens } from '@fluentui/react-components';
import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from './useAuth';

const useStyles = makeStyles({
  loading: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
  },
});

/**
 * Layout route that gates its children on authentication. While the
 * initial GET /v1/auth/me call is in flight we show a spinner so we
 * don't flash the login page. Once resolved, unauthenticated users are
 * redirected to /login with the requested path in history state for a
 * future return-to flow.
 */
export default function RequireAuth() {
  const styles = useStyles();
  const { user, loading } = useAuth();
  const location = useLocation();

  if (loading) {
    return (
      <div className={styles.loading}>
        <Spinner size="large" label="Restoring session..." />
      </div>
    );
  }

  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  return <Outlet />;
}
