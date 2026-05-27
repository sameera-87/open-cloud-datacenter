import { Button, Card, Spinner, Text, makeStyles, tokens } from '@fluentui/react-components';
import { useEffect, useReducer, useRef } from 'react';
import { Outlet } from 'react-router-dom';
import { makeApiClient } from '../api/client';
import { useAuth } from './useAuth';
import NoTenantsPage from '../pages/NoTenantsPage';

const useStyles = makeStyles({
  loading: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
  },
  errorWrap: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  errorCard: {
    maxWidth: '480px',
    width: '100%',
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXL}`,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    boxShadow: tokens.shadow16,
  },
});

type GateState =
  | { status: 'loading'; attempt: number }
  | { status: 'error'; attempt: number }
  | { status: 'empty'; attempt: number }
  | { status: 'ready'; attempt: number };

type GateAction =
  | { type: 'retry' }
  | { type: 'resolve'; tenantCount: number }
  | { type: 'reject' };

function gateReducer(state: GateState, action: GateAction): GateState {
  switch (action.type) {
    case 'retry':
      return { status: 'loading', attempt: state.attempt + 1 };
    case 'resolve':
      return { status: action.tenantCount === 0 ? 'empty' : 'ready', attempt: state.attempt };
    case 'reject':
      return { status: 'error', attempt: state.attempt };
  }
}

/**
 * Layout route that gates its children on the user having at least one
 * accessible tenant per GET /v1/tenants (DB-backed, not JWT-derived).
 *
 * Admins are NOT bypassed here — an admin with no tenants registered yet
 * should land on the admin variant of NoTenantsPage so they can create
 * the first tenant, not fall through to an empty TenantPickerPage.
 */
export default function RequireTenants() {
  const styles = useStyles();
  const { user } = useAuth();

  const [gate, dispatch] = useReducer(gateReducer, { status: 'loading', attempt: 0 });
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!user) return;

    // Cancel any in-flight fetch from a previous render.
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    const client = makeApiClient();

    void client
      .GET('/v1/tenants', { signal: controller.signal } as Parameters<typeof client.GET>[1])
      .then(({ data, response }) => {
        if (controller.signal.aborted) return;
        if (!response.ok) {
          dispatch({ type: 'reject' });
          return;
        }
        dispatch({ type: 'resolve', tenantCount: (data ?? []).length });
      })
      .catch(() => {
        if (!controller.signal.aborted) dispatch({ type: 'reject' });
      });

    return () => {
      controller.abort();
    };
    // gate.attempt drives re-fetches on retry; user drives re-fetches on login change.
  }, [user, gate.attempt]);

  // RequireAuth above us already handles the loading/unauthenticated states;
  // we only render once user is non-null.
  if (!user) return null;

  if (gate.status === 'loading') {
    return (
      <div className={styles.loading}>
        <Spinner size="large" label="Loading tenants…" />
      </div>
    );
  }

  if (gate.status === 'error') {
    return (
      <div className={styles.errorWrap}>
        <Card className={styles.errorCard}>
          <Text weight="semibold" size={500}>Could not load tenants</Text>
          <Text>There was a problem reaching the API. Check your connection and try again.</Text>
          <Button appearance="primary" onClick={() => dispatch({ type: 'retry' })}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }

  if (gate.status === 'empty') {
    // NoTenantsPage reads user.isAdmin internally and renders the correct variant.
    return <NoTenantsPage />;
  }

  return <Outlet />;
}
